/*
	Pulls data from multiple sources and funnels into InfluxDB.
*/

package main

import (
	"flag"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"github.com/skia-dev/glog"
	"go.skia.org/infra/go/auth"
	"go.skia.org/infra/go/buildbot"
	"go.skia.org/infra/go/common"
	"go.skia.org/infra/go/git/repograph"
	"go.skia.org/infra/go/gs"
	"go.skia.org/infra/go/influxdb"
	"go.skia.org/infra/go/metrics2"
	"go.skia.org/infra/go/swarming"
	"go.skia.org/infra/go/util"
	"golang.org/x/net/context"
	"google.golang.org/api/option"
)

const (
	MEASUREMENT_BUILDS_DURATION               = "buildbot.builds.duration"
	MEASUREMENT_BUILDS_FAILURE                = "buildbot.builds.failure-status"
	MEASUREMENT_BUILDS_INGESTED               = "buildbot.builds.ingested"
	MEASUREMENT_BUILDS_TOTAL                  = "buildbot.builds.total"
	MEASUREMENT_BUILDSLAVES_CONNECTED         = "buildbot.buildslaves.connected"
	MEASUREMENT_BUILDSTEPS_DURATION           = "buildbot.buildsteps.duration"
	MEASUREMENT_BUILDSTEPS_FAILURE            = "buildbot.buildsteps.failure-status"
	MEASUREMENT_SWARM_BOTS_LAST_SEEN          = "buildbot.swarm-bots.last-seen"
	MEASUREMENT_SWARM_BOTS_QUARANTINED        = "buildbot.swarm-bots.quarantined"
	MEASUREMENT_SWARM_TASKS_DURATION          = "swarming.tasks.duration"
	MEASUREMENT_SWARM_TASKS_OVERHEAD_BOT      = "swarming.tasks.overhead.bot"
	MEASUREMENT_SWARM_TASKS_OVERHEAD_DOWNLOAD = "swarming.tasks.overhead.download"
	MEASUREMENT_SWARM_TASKS_OVERHEAD_UPLOAD   = "swarming.tasks.overhead.upload"
	MEASUREMENT_SWARM_TASKS_PENDING_TIME      = "swarming.tasks.pending-time"
)

// flags
var (
	workdir  = flag.String("workdir", ".", "Working directory used by data processors.")
	local    = flag.Bool("local", false, "Running locally if true. As opposed to in production.")
	grpcPort = flag.String("grpc_port", ":8000", "Port on which to run the buildbot data gRPC server.")
	httpPort = flag.String("http_port", ":8001", "Port on which to run the HTTP server.")

	influxHost     = flag.String("influxdb_host", influxdb.DEFAULT_HOST, "The InfluxDB hostname.")
	influxUser     = flag.String("influxdb_name", influxdb.DEFAULT_USER, "The InfluxDB username.")
	influxPassword = flag.String("influxdb_password", influxdb.DEFAULT_PASSWORD, "The InfluxDB password.")
	influxDatabase = flag.String("influxdb_database", influxdb.DEFAULT_DATABASE, "The InfluxDB database.")

	// Regexp matching non-alphanumeric characters.
	re = regexp.MustCompile("[^A-Za-z0-9]+")

	BUILDSLAVE_OFFLINE_BLACKLIST = []string{
		"build3-a3",
		"build4-a3",
		"vm255-m3",
	}
)

// fixName transforms names of builders/buildsteps into strings useable by
// InfluxDB.
func fixName(s string) string {
	return re.ReplaceAllString(s, "_")
}

func main() {
	defer common.LogPanic()

	// Global init to initialize glog and parse arguments.
	common.InitWithMetrics2("datahopper", influxHost, influxUser, influxPassword, influxDatabase, local)

	// Absolutify the workdir.
	w, err := filepath.Abs(*workdir)
	if err != nil {
		glog.Fatal(w)
	}
	glog.Infof("Workdir is %s", w)

	// Authenticated HTTP client.
	oauthCacheFile := path.Join(w, "google_storage_token.data")
	httpClient, err := auth.NewClient(*local, oauthCacheFile, swarming.AUTH_SCOPE)
	if err != nil {
		glog.Fatal(err)
	}

	// Swarming API client.
	swarm, err := swarming.NewApiClient(httpClient)
	if err != nil {
		glog.Fatal(err)
	}

	// Shared repo objects.
	reposDir := path.Join(w, "repos")
	if err := os.MkdirAll(reposDir, os.ModePerm); err != nil {
		glog.Fatal(err)
	}
	repos, err := repograph.NewMap([]string{common.REPO_SKIA, common.REPO_SKIA_INFRA}, reposDir)
	if err != nil {
		glog.Fatal(err)
	}
	if err := repos.Update(); err != nil {
		glog.Fatal(err)
	}

	// Data generation goroutines.
	db, err := buildbot.NewLocalDB(path.Join(w, "buildbot.db"))
	if err != nil {
		glog.Fatal(err)
	}

	// Buildbot data ingestion.
	if err := buildbot.IngestNewBuildsLoop(db, repos); err != nil {
		glog.Fatal(err)
	}

	// Run a server for the buildbot data.
	if _, err := buildbot.RunBuildServer(*grpcPort, db); err != nil {
		glog.Fatal(err)
	}

	// Measure buildbot data ingestion progress.
	totalGauge := metrics2.GetInt64Metric(MEASUREMENT_BUILDS_TOTAL, nil)
	ingestGuage := metrics2.GetInt64Metric(MEASUREMENT_BUILDS_INGESTED, nil)
	go func() {
		for _ = range time.Tick(common.SAMPLE_PERIOD) {
			totalBuilds, err := buildbot.NumTotalBuilds()
			if err != nil {
				glog.Error(err)
				continue
			}
			ingestedBuilds, err := db.NumIngestedBuilds()
			if err != nil {
				glog.Error(err)
				continue
			}
			totalGauge.Update(int64(totalBuilds))
			ingestGuage.Update(int64(ingestedBuilds))
		}
	}()

	// Average build and step duration, failure rate.
	go func() {
		start := time.Now().Add(-1 * time.Hour)
		for _ = range time.Tick(10 * time.Minute) {
			end := time.Now().UTC()
			glog.Info("Loading build and buildstep duration data from %s to %s", start, end)
			builds, err := db.GetBuildsFromDateRange(start, end)
			if err != nil {
				glog.Errorf("Failed to obtain build and buildstep duration data: %s", err)
				continue
			}
			for _, b := range builds {
				if !b.IsFinished() {
					continue
				}
				tags := map[string]string{
					"builder":    b.Builder,
					"buildslave": b.BuildSlave,
					"master":     b.Master,
					"number":     strconv.Itoa(b.Number),
				}
				builderNameParts, err := buildbot.ParseBuilderName(b.Builder)
				if err != nil {
					glog.Warningf("Failed to parse builder name %q: %s", b.Builder, err)
					builderNameParts = map[string]string{}
				}
				for k, v := range builderNameParts {
					tags[k] = v
				}
				// Report build duration.
				d := b.Finished.Sub(b.Started)
				metrics2.RawAddInt64PointAtTime(MEASUREMENT_BUILDS_DURATION, tags, int64(d), b.Finished)

				// Report build failure rate.
				failureStatus := 0
				if b.Results != buildbot.BUILDBOT_SUCCESS {
					failureStatus = 1
				}
				metrics2.RawAddInt64PointAtTime(MEASUREMENT_BUILDS_FAILURE, tags, int64(failureStatus), b.Finished)

				for _, s := range b.Steps {
					if !s.IsFinished() {
						continue
					}
					d := s.Finished.Sub(s.Started)
					stepTags := make(map[string]string, len(tags)+1)
					for k, v := range tags {
						stepTags[k] = v
					}
					stepTags["step"] = s.Name
					// Report step duration.
					metrics2.RawAddInt64PointAtTime(MEASUREMENT_BUILDSTEPS_DURATION, stepTags, int64(d), s.Finished)

					// Report step failure rate.
					stepFailStatus := 0
					if s.Results != buildbot.BUILDBOT_SUCCESS {
						stepFailStatus = 1
					}
					metrics2.RawAddInt64PointAtTime(MEASUREMENT_BUILDSTEPS_FAILURE, stepTags, int64(stepFailStatus), s.Finished)
				}
			}
			start = end
		}
	}()

	// Offline buildslaves.
	go func() {
		lastKnownSlaves := map[string]string{}
		for _ = range time.Tick(time.Minute) {
			glog.Info("Loading buildslave data.")
			slaves, err := buildbot.GetBuildSlaves()
			if err != nil {
				glog.Error(err)
				continue
			}
			currentSlaves := map[string]string{}
			for masterName, m := range slaves {
				for _, s := range m {
					delete(lastKnownSlaves, s.Name)
					currentSlaves[s.Name] = masterName
					if util.In(s.Name, BUILDSLAVE_OFFLINE_BLACKLIST) {
						continue
					}
					v := int64(0)
					if s.Connected {
						v = int64(1)
					}
					metrics2.GetInt64Metric(MEASUREMENT_BUILDSLAVES_CONNECTED, map[string]string{
						"buildslave": s.Name,
						"master":     masterName,
					}).Update(v)
				}
			}
			// Delete metrics for slaves which have disappeared.
			for slave, master := range lastKnownSlaves {
				glog.Infof("Removing metric for buildslave %s", slave)
				if err := metrics2.GetInt64Metric(MEASUREMENT_BUILDSLAVES_CONNECTED, map[string]string{
					"buildslave": slave,
					"master":     master,
				}).Delete(); err != nil {
					glog.Errorf("Failed to delete metric: %s", err)
				}
			}
			lastKnownSlaves = currentSlaves
		}
	}()

	// Swarming bots.
	go func() {
		oldMetrics := []*metrics2.Int64Metric{}
		for _ = range time.Tick(2 * time.Minute) {
			glog.Info("Loading Skia Swarming bot data.")
			skiaBots, err := swarm.ListSkiaBots()
			if err != nil {
				glog.Error(err)
				continue
			}
			glog.Info("Loading SkiaTriggers Swarming bot data.")
			skiaTriggers, err := swarm.ListSkiaTriggerBots()
			if err != nil {
				glog.Error(err)
				continue
			}
			glog.Info("Loading CT Swarming bot data.")
			ctBots, err := swarm.ListCTBots()
			if err != nil {
				glog.Error(err)
				continue
			}
			bots := append(skiaBots, skiaTriggers...)
			bots = append(bots, ctBots...)

			// Delete old metrics, replace with new ones. This fixes the case where
			// bots are removed but their metrics hang around, or where dimensions
			// change resulting in duplicate metrics with the same bot ID.
			failedDelete := []*metrics2.Int64Metric{}
			for _, m := range oldMetrics {
				if err := m.Delete(); err != nil {
					glog.Warningf("Failed to delete metric: %s", err)
					failedDelete = append(failedDelete, m)
				}
			}
			oldMetrics = append([]*metrics2.Int64Metric{}, failedDelete...)

			now := time.Now()
			for _, bot := range bots {
				last, err := time.Parse("2006-01-02T15:04:05", bot.LastSeenTs)
				if err != nil {
					glog.Error(err)
					continue
				}

				tags := map[string]string{
					"bot": bot.BotId,
				}
				for _, d := range bot.Dimensions {
					tags[fmt.Sprintf("dimension-%s", d.Key)] = strings.Join(d.Value, ",")
				}

				// Bot last seen <duration> ago.
				m1 := metrics2.GetInt64Metric(MEASUREMENT_SWARM_BOTS_LAST_SEEN, tags)
				m1.Update(int64(now.Sub(last)))
				oldMetrics = append(oldMetrics, m1)

				// Bot quarantined status.
				quarantined := int64(0)
				if bot.Quarantined {
					quarantined = int64(1)
				}
				m2 := metrics2.GetInt64Metric(MEASUREMENT_SWARM_BOTS_QUARANTINED, tags)
				m2.Update(quarantined)
				oldMetrics = append(oldMetrics, m2)
			}
		}
	}()

	// Swarming tasks.
	go func() {
		// Initial query: load data from the past 2 hours.
		lastLoad := time.Now().Add(-2 * time.Hour)

		revisitTasks := map[string]bool{}

		for _ = range time.Tick(2 * time.Minute) {
			now := time.Now()
			tasks, err := swarm.ListSkiaTasks(lastLoad, now)
			if err != nil {
				glog.Error(err)
				continue
			}
			for id, _ := range revisitTasks {
				task, err := swarm.GetTaskMetadata(id)
				if err != nil {
					glog.Error(err)
					continue
				}
				tasks = append(tasks, task)
			}
			revisitTasks = map[string]bool{}
			lastLoad = now
			for _, task := range tasks {
				if task.TaskResult.State == "COMPLETED" {
					if task.TaskResult.DedupedFrom != "" {
						continue
					}

					// Get the created time for the task. We'll use that as the
					// timestamp for all data points related to it.
					createdTime, err := swarming.Created(task)
					if err != nil {
						glog.Errorf("Failed to parse Swarming task created timestamp: %s", err)
						continue
					}

					// Find the tags for the task, including ID, name, dimensions,
					// and components of the builder name.
					name, err := swarming.GetTagValue(task.TaskResult, "name")
					if err != nil || name == "" {
						glog.Errorf("Failed to find name for Swarming task: %v", task)
						continue
					}
					builderName, err := swarming.GetTagValue(task.TaskResult, "buildername")
					if err != nil || builderName == "" {
						glog.Errorf("Failed to find buildername for Swarming task: %v", task)
						continue
					}
					builderTags, err := buildbot.ParseBuilderName(builderName)
					if err != nil {
						glog.Errorf("Failed to parse builder name for Swarming task: %s", err)
						continue
					}

					tags := map[string]string{
						"bot-id":    task.TaskResult.BotId,
						"task-id":   task.TaskId,
						"task-name": name,
					}
					for _, d := range task.Request.Properties.Dimensions {
						tags[fmt.Sprintf("dimension-%s", d.Key)] = d.Value
					}
					for k, v := range builderTags {
						tags[k] = v
					}

					// Task duration in milliseconds.
					metrics2.RawAddInt64PointAtTime(MEASUREMENT_SWARM_TASKS_DURATION, tags, int64(task.TaskResult.Duration*float64(1000.0)), createdTime)

					if task.TaskResult.PerformanceStats != nil {
						// Overhead stats, in milliseconds.
						metrics2.RawAddInt64PointAtTime(MEASUREMENT_SWARM_TASKS_OVERHEAD_BOT, tags, int64(task.TaskResult.PerformanceStats.BotOverhead*float64(1000.0)), createdTime)
						if task.TaskResult.PerformanceStats.IsolatedDownload != nil {
							metrics2.RawAddInt64PointAtTime(MEASUREMENT_SWARM_TASKS_OVERHEAD_DOWNLOAD, tags, int64(task.TaskResult.PerformanceStats.IsolatedDownload.Duration*float64(1000.0)), createdTime)
						} else {
							glog.Errorf("Swarming task is missing its IsolatedDownload section: %v", task.TaskResult)
						}
						if task.TaskResult.PerformanceStats.IsolatedUpload != nil {
							metrics2.RawAddInt64PointAtTime(MEASUREMENT_SWARM_TASKS_OVERHEAD_UPLOAD, tags, int64(task.TaskResult.PerformanceStats.IsolatedUpload.Duration*float64(1000.0)), createdTime)
						} else {
							glog.Errorf("Swarming task is missing its IsolatedUpload section: %v", task.TaskResult)
						}
					}

					// Pending time in milliseconds.
					startTime, err := swarming.Started(task)
					if err != nil {
						glog.Errorf("Failed to parse Swarming task started timestamp: %s", err)
						continue
					}
					pendingMs := int64(startTime.Sub(createdTime).Seconds() * float64(1000.0))
					metrics2.RawAddInt64PointAtTime(MEASUREMENT_SWARM_TASKS_PENDING_TIME, tags, pendingMs, createdTime)
				} else {
					revisitTasks[task.TaskId] = true
				}
			}
		}
	}()

	// Number of commits in the repo.
	go func() {
		skiaGauge := metrics2.GetInt64Metric("repo.commits", map[string]string{"repo": "skia"})
		infraGauge := metrics2.GetInt64Metric("repo.commits", map[string]string{"repo": "infra"})
		for _ = range time.Tick(5 * time.Minute) {
			nSkia, err := repos[common.REPO_SKIA].Repo().NumCommits()
			if err != nil {
				glog.Errorf("Failed to get number of commits for Skia: %s", err)
			} else {
				skiaGauge.Update(nSkia)
			}
			nInfra, err := repos[common.REPO_SKIA_INFRA].Repo().NumCommits()
			if err != nil {
				glog.Errorf("Failed to get number of commits for Infra: %s", err)
			} else {
				infraGauge.Update(nInfra)
			}
		}
	}()

	// Time since last successful backup.
	go func() {
		lv := metrics2.NewLiveness("last-buildbot-db-backup", nil)
		authClient, err := auth.NewDefaultJWTServiceAccountClient(auth.SCOPE_READ_ONLY)
		if err != nil {
			glog.Fatal(err)
		}
		gsClient, err := storage.NewClient(context.Background(), option.WithHTTPClient(authClient))
		if err != nil {
			glog.Fatal(err)
		}
		setLastBackupTime := func() error {
			last := time.Time{}
			if err := gs.AllFilesInDir(gsClient, "skia-buildbots", "db_backup", func(item *storage.ObjectAttrs) {
				if item.Updated.After(last) {
					last = item.Updated
				}
			}); err != nil {
				return err
			}
			lv.ManualReset(last)
			glog.Infof("Last DB backup was %s.", last)
			return nil
		}
		if err := setLastBackupTime(); err != nil {
			glog.Fatal(err)
		}
		for _ = range time.Tick(10 * time.Minute) {
			if err := setLastBackupTime(); err != nil {
				glog.Errorf("Failed to get last DB backup time: %s", err)
			}
		}
	}()

	// Run a backup server.
	go func() {
		glog.Fatal(buildbot.RunBackupServer(db, *httpPort))
	}()

	// Wait while the above goroutines generate data.
	select {}
}
