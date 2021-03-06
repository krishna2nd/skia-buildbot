package recovery

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"math/rand"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/storage"
	"golang.org/x/net/context"
	"google.golang.org/api/option"

	"github.com/gorilla/mux"
	assert "github.com/stretchr/testify/require"
	"go.skia.org/infra/go/exec"
	exec_testutils "go.skia.org/infra/go/exec/testutils"
	"go.skia.org/infra/go/mockhttpclient"
	"go.skia.org/infra/go/testutils"
	"go.skia.org/infra/go/util"
	"go.skia.org/infra/task_scheduler/go/db"
)

const (
	TEST_BUCKET     = "skia-test"
	TEST_DB_CONTENT = `
I'm a little database
Short and stout!
Here is my file handle
Here is my timeout.
When I get all locked up,
Hear me shout!
Make a backup
And write it out!
`
	TEST_DB_TIME         = 1477000000
	TEST_DB_CONTENT_SEED = 299792
)

// Create an io.Reader that returns the given number of bytes.
func makeLargeDBContent(bytes int64) io.Reader {
	r := rand.New(rand.NewSource(TEST_DB_CONTENT_SEED))
	return &io.LimitedReader{
		R: r,
		N: bytes,
	}
}

// testDB implements db.BackupDBCloser.
type testDB struct {
	db.DB
	content          io.Reader
	injectGetTSError error
	injectWriteError error
}

// Closes content if necessary.
func (tdb *testDB) Close() error {
	closer, ok := tdb.content.(io.Closer)
	if ok {
		return closer.Close()
	}
	return nil
}

// Implements BackupDBCloser.WriteBackup.
func (tdb *testDB) WriteBackup(w io.Writer) error {
	defer util.Close(tdb) // close tdb.content
	if tdb.injectWriteError != nil {
		return tdb.injectWriteError
	}
	_, err := io.Copy(w, tdb.content)
	return err
}

// Implements BackupDBCloser.SetIncrementalBackupTime.
func (*testDB) SetIncrementalBackupTime(time.Time) error {
	return nil
}

// Implements BackupDBCloser.GetIncrementalBackupTime.
func (tdb *testDB) GetIncrementalBackupTime() (time.Time, error) {
	if tdb.injectGetTSError != nil {
		return time.Time{}, tdb.injectGetTSError
	}
	return time.Unix(TEST_DB_TIME, 0).UTC(), nil
}

// getMockedDBBackup returns a gsDBBackup that handles GS requests with mockMux.
// If mockMux is nil, an empty mux.Router is used. WriteBackup will write
// TEST_DB_CONTENT.
func getMockedDBBackup(t *testing.T, mockMux *mux.Router) (*gsDBBackup, context.CancelFunc) {
	return getMockedDBBackupWithContent(t, mockMux, bytes.NewReader([]byte(TEST_DB_CONTENT)))
}

// getMockedDBBackupWithContent is like getMockedDBBackup but WriteBackup will
// copy the given content.
func getMockedDBBackupWithContent(t *testing.T, mockMux *mux.Router, content io.Reader) (*gsDBBackup, context.CancelFunc) {
	if mockMux == nil {
		mockMux = mux.NewRouter()
	}
	ctx, ctxCancel := context.WithCancel(context.Background())
	gsClient, err := storage.NewClient(ctx, option.WithHTTPClient(mockhttpclient.NewMuxClient(mockMux)))
	assert.NoError(t, err)

	dir, err := ioutil.TempDir("", "getMockedDBBackupWithContent")
	assert.NoError(t, err)
	assert.NoError(t, os.MkdirAll(path.Join(dir, TRIGGER_DIRNAME), os.ModePerm))

	db := &testDB{
		DB:      db.NewInMemoryDB(),
		content: content,
	}
	b, err := newGsDbBackupWithClient(ctx, TEST_BUCKET, db, "task_scheduler_db", dir, gsClient)
	assert.NoError(t, err)
	return b, func() {
		ctxCancel()
		testutils.RemoveAll(t, dir)
	}
}

// object represents a GS object for makeObjectResponse and makeObjectsResponse.
type object struct {
	bucket string
	name   string
	time   time.Time
}

// makeObjectResponse generates the JSON representation of a GS object.
func makeObjectResponse(obj object) string {
	timeStr := obj.time.UTC().Format(time.RFC3339)
	return fmt.Sprintf(`{
  "kind": "storage#object",
  "id": "%s/%s",
  "name": "%s",
  "bucket": "%s",
  "generation": "1",
  "metageneration": "1",
  "timeCreated": "%s",
  "updated": "%s",
  "storageClass": "STANDARD",
  "size": "15",
  "md5Hash": "d8dh5MIGdPoMfh/owveXhA==",
  "crc32c": "Oz54cA==",
  "etag": "CLD56dvBp8oCEAE="
}`, obj.bucket, obj.name, obj.name, obj.bucket, timeStr, timeStr)
}

// makeObjectsResponse generates the JSON representation of an array of GS
// objects.
func makeObjectsResponse(objs []object) string {
	jsObjs := make([]string, 0, len(objs))
	for _, o := range objs {
		jsObjs = append(jsObjs, makeObjectResponse(o))
	}
	return fmt.Sprintf(`{
  "kind": "storage#objects",
  "items": [
%s
  ]
}`, strings.Join(jsObjs, ",\n"))
}

// gsRoute returns the mux.Route for the GS server.
func gsRoute(mockMux *mux.Router) *mux.Route {
	return mockMux.Schemes("https").Host("www.googleapis.com")
}

// getBackupMetrics should return zero time and zero count when there are no
// existing backups.
func TestGetBackupMetricsNoFiles(t *testing.T) {
	testutils.SmallTest(t)
	now := time.Now()
	r := mux.NewRouter()
	gsRoute(r).Methods("GET").
		Path(fmt.Sprintf("/storage/v1/b/%s/o", TEST_BUCKET)).
		Queries("prefix", DB_BACKUP_DIR).
		Handler(mockhttpclient.MockGetDialogue([]byte(makeObjectsResponse([]object{}))))
	b, cancel := getMockedDBBackup(t, r)
	defer cancel()

	ts, count, err := b.getBackupMetrics(now)
	assert.NoError(t, err)
	assert.True(t, ts.IsZero())
	assert.Equal(t, int64(0), count)
}

// getBackupMetrics should return the time of the latest object when there are
// multiple.
func TestGetBackupMetricsTwoFiles(t *testing.T) {
	testutils.SmallTest(t)
	now := time.Now().Round(time.Second)
	r := mux.NewRouter()
	gsRoute(r).Methods("GET").
		Path(fmt.Sprintf("/storage/v1/b/%s/o", TEST_BUCKET)).
		Queries("prefix", DB_BACKUP_DIR).
		Handler(mockhttpclient.MockGetDialogue([]byte(makeObjectsResponse([]object{
			{TEST_BUCKET, "a", now.Add(-1 * time.Hour).UTC()},
			{TEST_BUCKET, "b", now.Add(-2 * time.Hour).UTC()},
		}))))
	b, cancel := getMockedDBBackup(t, r)
	defer cancel()

	ts, count, err := b.getBackupMetrics(now)
	assert.NoError(t, err)
	assert.True(t, ts.Equal(now.Add(-1*time.Hour)), "Expected %s, got %s", now.Add(-1*time.Hour), ts)
	assert.Equal(t, int64(2), count)
}

// getBackupMetrics should not count objects that were not modified recently.
func TestGetBackupMetricsSeveralDays(t *testing.T) {
	testutils.SmallTest(t)
	now := time.Now().Round(time.Second)
	r := mux.NewRouter()
	gsRoute(r).Methods("GET").
		Path(fmt.Sprintf("/storage/v1/b/%s/o", TEST_BUCKET)).
		Queries("prefix", DB_BACKUP_DIR).
		Handler(mockhttpclient.MockGetDialogue([]byte(makeObjectsResponse([]object{
			{TEST_BUCKET, "a", now.Add(-49 * time.Hour).UTC()},
			{TEST_BUCKET, "b", now.Add(-25 * time.Hour).UTC()},
			{TEST_BUCKET, "c", now.Add(-1 * time.Hour).UTC()},
		}))))
	b, cancel := getMockedDBBackup(t, r)
	defer cancel()

	ts, count, err := b.getBackupMetrics(now)
	assert.NoError(t, err)
	assert.True(t, ts.Equal(now.Add(-1*time.Hour)))
	assert.Equal(t, int64(1), count)
}

// getBackupMetrics should return the latest backup time even if it is far in
// the past.
func TestGetBackupMetricsOld(t *testing.T) {
	testutils.SmallTest(t)
	now := time.Now().Round(time.Second)
	r := mux.NewRouter()
	gsRoute(r).Methods("GET").
		Path(fmt.Sprintf("/storage/v1/b/%s/o", TEST_BUCKET)).
		Queries("prefix", DB_BACKUP_DIR).
		Handler(mockhttpclient.MockGetDialogue([]byte(makeObjectsResponse([]object{
			{TEST_BUCKET, "a", now.Add(-49 * time.Hour).UTC()},
			{TEST_BUCKET, "b", now.Add(-128 * time.Hour).UTC()},
			{TEST_BUCKET, "c", now.Add(-762 * time.Hour).UTC()},
		}))))
	b, cancel := getMockedDBBackup(t, r)
	defer cancel()

	ts, count, err := b.getBackupMetrics(now)
	assert.NoError(t, err)
	assert.True(t, ts.Equal(now.Add(-49*time.Hour)))
	assert.Equal(t, int64(0), count)
}

// writeDBBackupToFile should produce a file with contents equal to what
// WriteBackup wrote.
func TestWriteDBBackupToFile(t *testing.T) {
	testutils.SmallTest(t)
	b, cancel := getMockedDBBackup(t, nil)
	defer cancel()

	tempdir, err := ioutil.TempDir("", "backups_test")
	assert.NoError(t, err)
	defer testutils.RemoveAll(t, tempdir)

	filename := path.Join(tempdir, "foo.bdb")
	err = b.writeDBBackupToFile(filename)
	assert.NoError(t, err)

	actualContents, err := ioutil.ReadFile(filename)
	assert.NoError(t, err)
	assert.Equal(t, TEST_DB_CONTENT, string(actualContents))
}

// writeDBBackupToFile should succeed even if GetIncrementalBackupTime returns
// an error.
func TestWriteDBBackupToFileGetIncrementalBackupTimeError(t *testing.T) {
	testutils.SmallTest(t)
	b, cancel := getMockedDBBackup(t, nil)
	defer cancel()

	tempdir, err := ioutil.TempDir("", "backups_test")
	assert.NoError(t, err)
	defer testutils.RemoveAll(t, tempdir)

	injectedError := fmt.Errorf("Not giving you the time of day!")
	// This should not prevent DB from being backed up.
	b.db.(*testDB).injectGetTSError = injectedError
	filename := path.Join(tempdir, "foo.bdb")
	err = b.writeDBBackupToFile(filename)
	assert.NoError(t, err)

	actualContents, err := ioutil.ReadFile(filename)
	assert.NoError(t, err)
	assert.Equal(t, TEST_DB_CONTENT, string(actualContents))
}

// writeDBBackupToFile could fail due to disk error.
func TestWriteDBBackupToFileCreateError(t *testing.T) {
	testutils.SmallTest(t)
	b, cancel := getMockedDBBackup(t, nil)
	defer cancel()

	tempdir, err := ioutil.TempDir("", "backups_test")
	assert.NoError(t, err)
	defer testutils.RemoveAll(t, tempdir)

	filename := path.Join(tempdir, "nonexistant_dir", "foo.bdb")
	err = b.writeDBBackupToFile(filename)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "Could not create temp file to write DB backup")
}

// writeDBBackupToFile could fail due to DB error.
func TestWriteDBBackupToFileDBError(t *testing.T) {
	testutils.SmallTest(t)
	b, cancel := getMockedDBBackup(t, nil)
	defer cancel()

	tempdir, err := ioutil.TempDir("", "backups_test")
	assert.NoError(t, err)
	defer testutils.RemoveAll(t, tempdir)

	injectedError := fmt.Errorf("Can't back up: unable to shift to reverse.")
	b.db.(*testDB).injectWriteError = injectedError
	filename := path.Join(tempdir, "foo.bdb")
	err = b.writeDBBackupToFile(filename)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), injectedError.Error())
}

// addMultipartHandler causes r to respond to a request to add an object with
// the given name to TEST_BUCKET with a successful response and sets
// actualBytesGzip to the object contents. Also performs assertions on the
// request.
func addMultipartHandler(t *testing.T, r *mux.Router, name string, actualBytesGzip *[]byte) {
	gsRoute(r).Methods("POST").Path(fmt.Sprintf("/upload/storage/v1/b/%s/o", TEST_BUCKET)).
		Queries("uploadType", "multipart").
		HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t := mockhttpclient.MuxSafeT(t)
			mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			assert.NoError(t, err)
			assert.Equal(t, "multipart/related", mediaType)
			mr := multipart.NewReader(r.Body, params["boundary"])
			jsonPart, err := mr.NextPart()
			assert.NoError(t, err)
			data := map[string]string{}
			assert.NoError(t, json.NewDecoder(jsonPart).Decode(&data))
			assert.Equal(t, TEST_BUCKET, data["bucket"])
			assert.Equal(t, name, data["name"])
			assert.Equal(t, "application/octet-stream", data["contentType"])
			assert.Equal(t, "gzip", data["contentEncoding"])
			assert.Equal(t, fmt.Sprintf("attachment; filename=\"%s\"", path.Base(name)), data["contentDisposition"])
			dataPart, err := mr.NextPart()
			assert.NoError(t, err)
			*actualBytesGzip, err = ioutil.ReadAll(dataPart)
			assert.NoError(t, err)
			_, _ = w.Write([]byte(makeObjectResponse(object{TEST_BUCKET, name, time.Now()})))
		})
}

// uploadFile should upload a file to GS.
func TestUploadFile(t *testing.T) {
	testutils.SmallTest(t)
	tempdir, err := ioutil.TempDir("", "backups_test")
	assert.NoError(t, err)
	defer testutils.RemoveAll(t, tempdir)

	filename := path.Join(tempdir, "myfile.txt")
	assert.NoError(t, ioutil.WriteFile(filename, []byte(TEST_DB_CONTENT), os.ModePerm))

	now := time.Now().Round(time.Second)
	r := mux.NewRouter()
	name := "path/to/gsfile.txt"
	var actualBytesGzip []byte
	addMultipartHandler(t, r, name, &actualBytesGzip)

	b, cancel := getMockedDBBackup(t, r)
	defer cancel()

	err = uploadFile(b.ctx, filename, b.gsClient.Bucket(b.gsBucket), name, now)
	assert.NoError(t, err)

	gzR, err := gzip.NewReader(bytes.NewReader(actualBytesGzip))
	assert.NoError(t, err)
	assert.True(t, now.Equal(gzR.Header.ModTime))
	assert.Equal(t, "myfile.txt", gzR.Header.Name)
	actualBytes, err := ioutil.ReadAll(gzR)
	assert.NoError(t, err)
	assert.NoError(t, gzR.Close())
	assert.Equal(t, TEST_DB_CONTENT, string(actualBytes))
}

// uploadFile may fail if the file doesn't exist.
func TestUploadFileNoFile(t *testing.T) {
	testutils.SmallTest(t)
	tempdir, err := ioutil.TempDir("", "backups_test")
	assert.NoError(t, err)
	defer testutils.RemoveAll(t, tempdir)

	filename := path.Join(tempdir, "myfile.txt")

	b, cancel := getMockedDBBackup(t, nil)
	defer cancel()

	now := time.Now()
	name := "path/to/gsfile.txt"
	err = uploadFile(b.ctx, filename, b.gsClient.Bucket(b.gsBucket), name, now)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "Unable to read temporary backup file")
}

// uploadFile may fail if the GS request fails.
func TestUploadFileUploadError(t *testing.T) {
	testutils.SmallTest(t)
	tempdir, err := ioutil.TempDir("", "backups_test")
	assert.NoError(t, err)
	defer testutils.RemoveAll(t, tempdir)

	filename := path.Join(tempdir, "myfile.txt")
	assert.NoError(t, ioutil.WriteFile(filename, []byte(TEST_DB_CONTENT), os.ModePerm))

	now := time.Now()
	r := mux.NewRouter()
	name := "path/to/gsfile.txt"

	gsRoute(r).Methods("POST").
		Path(fmt.Sprintf("/upload/storage/v1/b/%s/o", TEST_BUCKET)).
		Queries("uploadType", "multipart").
		HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			util.Close(r.Body)
			http.Error(w, "I don't like your poem.", http.StatusTeapot)
		})

	b, cancel := getMockedDBBackup(t, r)
	defer cancel()

	err = uploadFile(b.ctx, filename, b.gsClient.Bucket(b.gsBucket), name, now)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "got HTTP response code 418 with body: I don't like your poem.")
}

// backupDB should create a GS object with the gzipped contents of the DB.
func TestBackupDB(t *testing.T) {
	testutils.SmallTest(t)
	var expectedBytes []byte
	{
		// Get expectedBytes from writeDBBackupToFile.
		b, cancel := getMockedDBBackup(t, nil)
		defer cancel()

		tempdir, err := ioutil.TempDir("", "backups_test")
		assert.NoError(t, err)
		defer testutils.RemoveAll(t, tempdir)

		filename := path.Join(tempdir, "expected.bdb")
		err = b.writeDBBackupToFile(filename)
		assert.NoError(t, err)
		expectedBytes, err = ioutil.ReadFile(filename)
		assert.NoError(t, err)
	}

	now := time.Now()
	r := mux.NewRouter()
	name := DB_BACKUP_DIR + "/" + now.UTC().Format("2006/01/02") + "/task-scheduler.bdb"

	var actualBytesGzip []byte
	addMultipartHandler(t, r, name, &actualBytesGzip)

	b, cancel := getMockedDBBackup(t, r)
	defer cancel()

	err := b.backupDB(now, "task-scheduler")
	assert.NoError(t, err)
	gzR, err := gzip.NewReader(bytes.NewReader(actualBytesGzip))
	assert.NoError(t, err)
	actualBytes, err := ioutil.ReadAll(gzR)
	assert.NoError(t, err)
	assert.NoError(t, gzR.Close())
	assert.Equal(t, expectedBytes, actualBytes)
}

// testBackupDBLarge tests backupDB for DB contents larger than 8MB.
func testBackupDBLarge(t *testing.T, contentSize int64) {
	now := time.Now()
	r := mux.NewRouter()
	name := DB_BACKUP_DIR + "/" + now.UTC().Format("2006/01/02") + "/task-scheduler.bdb"

	// https://cloud.google.com/storage/docs/json_api/v1/how-tos/resumable-upload
	uploadId := "resume_me_please"
	gsRoute(r).Methods("POST").Path(fmt.Sprintf("/upload/storage/v1/b/%s/o", TEST_BUCKET)).
		Queries("uploadType", "resumable").
		Headers("Content-Type", "application/json").
		HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t := mockhttpclient.MuxSafeT(t)
			data := map[string]string{}
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&data))
			assert.Equal(t, TEST_BUCKET, data["bucket"])
			assert.Equal(t, name, data["name"])
			assert.Equal(t, "application/octet-stream", data["contentType"])
			assert.Equal(t, "gzip", data["contentEncoding"])
			assert.Equal(t, "attachment; filename=\"task-scheduler.bdb\"", data["contentDisposition"])
			uploadUrl, err := url.Parse(r.URL.String())
			assert.NoError(t, err)
			query := uploadUrl.Query()
			query.Set("upload_id", uploadId)
			uploadUrl.RawQuery = query.Encode()
			w.Header().Set("Location", uploadUrl.String())
		})

	rangeRegexp := regexp.MustCompile("bytes ([0-9]+|\\*)-?([0-9]+)?/([0-9]+|\\*)")

	var recvBytes int64 = 0
	complete := false

	// Despite what the documentation says, the Go client uses POST, not PUT.
	gsRoute(r).Methods("POST").Path(fmt.Sprintf("/upload/storage/v1/b/%s/o", TEST_BUCKET)).
		Queries("uploadType", "resumable", "upload_id", uploadId).
		Headers("Content-Type", "application/octet-stream").
		HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t := mockhttpclient.MuxSafeT(t)

			byteRange := rangeRegexp.FindStringSubmatch(r.Header.Get("Content-Range"))
			assert.Equal(t, 4, len(byteRange), "Unexpected request %v %s", r.Header, r.URL.String())

			assert.NotEqual(t, "*", byteRange[1], "Test does not support upload size that is a multiple of 8MB.")

			begin, err := strconv.ParseInt(byteRange[1], 10, 64)
			assert.NoError(t, err)
			assert.Equal(t, recvBytes, begin)

			end, err := strconv.ParseInt(byteRange[2], 10, 64)
			assert.NoError(t, err)

			finalChunk := false
			if byteRange[3] != "*" {
				size, err := strconv.ParseInt(byteRange[3], 10, 64)
				assert.NoError(t, err)
				finalChunk = size == end+1
			}

			recvBytes += end - begin + 1
			if finalChunk {
				complete = true
				_, _ = w.Write([]byte(makeObjectResponse(object{TEST_BUCKET, name, time.Now()})))
			} else {
				w.Header().Set("Range", fmt.Sprintf("0-%d", recvBytes-1))
				// https://github.com/google/google-api-go-client/commit/612451d2aabbf88084e4f1c48c0781073c0d5583
				w.Header().Set("X-HTTP-Status-Code-Override", "308")
				w.WriteHeader(200)
			}
		})

	b, cancel := getMockedDBBackupWithContent(t, r, makeLargeDBContent(contentSize))
	defer cancel()

	// Check available disk space.
	output, err := exec.RunCommand(&exec.Command{
		Name: "df",
		Args: []string{"--block-size=1", "--output=avail", os.TempDir()},
	})
	assert.NoError(t, err, "df failed: %s", output)
	// Output looks like:
	//       Avail
	// 13704458240
	availSize, err := strconv.ParseInt(strings.TrimSpace(strings.Split(output, "\n")[1]), 10, 64)
	assert.NoError(t, err, "Unable to parse df output: %s", output)
	assert.True(t, availSize > contentSize, "Insufficient disk space to run test; need %d bytes, have %d bytes for %s. Please set TMPDIR.", contentSize, availSize, os.TempDir())

	err = b.backupDB(now, "task-scheduler")
	assert.NoError(t, err)
	assert.True(t, complete)
}

// backupDB should work for a large-ish DB.
func TestBackupDBLarge(t *testing.T) {
	testutils.MediumTest(t)
	// Send 128MB. Add 1 so it's not a multiple of 8MB.
	var contentSize int64 = 128*1024*1024 + 1
	testBackupDBLarge(t, contentSize)
}

// backupDB should work for a 16GB DB.
func TestBackupDBHuge(t *testing.T) {
	t.Skipf("TODO(benjaminwagner): change TMPDIR to make this work.")
	testutils.LargeTest(t)
	// Send 16GB. Add 1 so it's not a multiple of 8MB.
	var contentSize int64 = 16*1024*1024*1024 + 1
	testBackupDBLarge(t, contentSize)
}

// immediateBackupBasename should return a name based on the time of day.
func TestImmediateBackupBasename(t *testing.T) {
	testutils.SmallTest(t)
	test := func(expected string, input time.Time) {
		assert.Equal(t, expected, immediateBackupBasename(input))
	}
	test("task-scheduler-00:00:00", time.Date(2016, 1, 1, 0, 0, 0, 0, time.UTC))
	test("task-scheduler-01:02:03", time.Date(2016, 2, 29, 1, 2, 3, 0, time.UTC))
	test("task-scheduler-13:14:15", time.Date(2016, 10, 27, 13, 14, 15, 16171819, time.UTC))
	test("task-scheduler-23:59:59", time.Date(2016, 12, 31, 23, 59, 59, 999999999, time.UTC))
}

// findAndParseTriggerFile should return an error when the directory doesn't
// exist.
func TestFindAndParseTriggerFileNoDir(t *testing.T) {
	testutils.SmallTest(t)
	b, cancel := getMockedDBBackup(t, nil)
	defer cancel()

	testutils.RemoveAll(t, b.triggerDir)
	_, _, err := b.findAndParseTriggerFile()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "Unable to read trigger directory")
}

// findAndParseTriggerFile should return empty for an empty dir.
func TestFindAndParseTriggerFileNoFile(t *testing.T) {
	testutils.SmallTest(t)
	b, cancel := getMockedDBBackup(t, nil)
	defer cancel()

	file, attempts, err := b.findAndParseTriggerFile()
	assert.NoError(t, err)
	assert.Equal(t, "", file)
	assert.Equal(t, 0, attempts)
}

// findAndParseTriggerFile should return the filename and indicate no attempts
// for an empty file.
func TestFindAndParseTriggerFileNewFile(t *testing.T) {
	testutils.SmallTest(t)
	b, cancel := getMockedDBBackup(t, nil)
	defer cancel()

	exec_testutils.Run(t, b.triggerDir, "touch", "foo")
	file, attempts, err := b.findAndParseTriggerFile()
	assert.NoError(t, err)
	assert.Equal(t, "foo", file)
	assert.Equal(t, 0, attempts)
}

// findAndParseTriggerFile should choose one of the files when multiple are
// present.
func TestFindAndParseTriggerFileTwoFiles(t *testing.T) {
	testutils.SmallTest(t)
	b, cancel := getMockedDBBackup(t, nil)
	defer cancel()

	exec_testutils.Run(t, b.triggerDir, "touch", "foo")
	exec_testutils.Run(t, b.triggerDir, "touch", "bar")
	file, attempts, err := b.findAndParseTriggerFile()
	assert.NoError(t, err)
	assert.True(t, file == "foo" || file == "bar")
	assert.Equal(t, 0, attempts)
}

// writeTriggerFile followed by findAndParseTriggerFile should return the same
// values.
func TestWriteFindAndParseTriggerFileWithRetries(t *testing.T) {
	testutils.SmallTest(t)
	b, cancel := getMockedDBBackup(t, nil)
	defer cancel()

	for i := 1; i < 3; i++ {
		assert.NoError(t, b.writeTriggerFile("foo", i))
		file, attempts, err := b.findAndParseTriggerFile()
		assert.NoError(t, err)
		assert.Equal(t, "foo", file)
		assert.Equal(t, i, attempts)
	}
}

// writeTriggerFile could fail if permissions are incorrect.
func TestWriteTriggerFileReadOnly(t *testing.T) {
	testutils.SmallTest(t)
	b, cancel := getMockedDBBackup(t, nil)
	defer cancel()

	assert.NoError(t, ioutil.WriteFile(path.Join(b.triggerDir, "foo"), []byte{}, 0444))
	err := b.writeTriggerFile("foo", 1)
	assert.Error(t, err)
	assert.Regexp(t, `Unable to write new retry count \(1\) to trigger file .*/foo: .*permission denied`, err.Error())
}

// findAndParseTriggerFile should return an error when the file can't be parsed.
func TestFindAndParseTriggerFileInvalidContents(t *testing.T) {
	testutils.SmallTest(t)
	b, cancel := getMockedDBBackup(t, nil)
	defer cancel()

	assert.NoError(t, ioutil.WriteFile(path.Join(b.triggerDir, "foo"), []byte("Hi Mom!"), 0666))
	_, _, err := b.findAndParseTriggerFile()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "Unable to parse trigger file")
}

// deleteTriggerFile followed by findAndParseTriggerFile should return empty.
func TestDeleteTriggerFile(t *testing.T) {
	testutils.SmallTest(t)
	b, cancel := getMockedDBBackup(t, nil)
	defer cancel()

	assert.NoError(t, b.writeTriggerFile("foo", 1))
	file, attempts, err := b.findAndParseTriggerFile()
	assert.NoError(t, err)
	assert.Equal(t, "foo", file)
	assert.Equal(t, 1, attempts)

	assert.NoError(t, b.deleteTriggerFile("foo"))
	file, attempts, err = b.findAndParseTriggerFile()
	assert.NoError(t, err)
	assert.Equal(t, "", file)
	assert.Equal(t, 0, attempts)

	files, err := ioutil.ReadDir(b.triggerDir)
	assert.NoError(t, err)
	assert.Equal(t, 0, len(files))
}

// deleteTriggerFile could fail if file has already been deleted.
func TestDeleteTriggerFileAlreadyDeleted(t *testing.T) {
	testutils.SmallTest(t)
	b, cancel := getMockedDBBackup(t, nil)
	defer cancel()

	err := b.deleteTriggerFile("foo")
	assert.Error(t, err)
	assert.Regexp(t, "Unable to remove trigger file .*/foo: .*no such file", err.Error())
}

// maybeBackupDB should do nothing if there is no trigger file.
func TestMaybeBackupDBNotYet(t *testing.T) {
	testutils.SmallTest(t)
	now := time.Now()
	r := mux.NewRouter()
	called := false
	gsRoute(r).HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})

	b, cancel := getMockedDBBackup(t, r)
	defer cancel()

	b.maybeBackupDB(now)
	assert.False(t, called)
}

// maybeBackupDB should find the trigger file and perform a backup, then delete
// the trigger file if successful.
func TestMaybeBackupDBSuccess(t *testing.T) {
	testutils.SmallTest(t)
	now := time.Date(2016, 10, 26, 5, 0, 0, 0, time.UTC)
	r := mux.NewRouter()
	name := DB_BACKUP_DIR + "/" + now.UTC().Format("2006/01/02") + "/task-scheduler.bdb"

	var actualBytesGzip []byte
	addMultipartHandler(t, r, name, &actualBytesGzip)

	b, cancel := getMockedDBBackup(t, r)
	defer cancel()

	assert.NoError(t, b.writeTriggerFile("task-scheduler", 0))

	b.maybeBackupDB(now)

	assert.True(t, len(actualBytesGzip) > 0)

	file, _, err := b.findAndParseTriggerFile()
	assert.NoError(t, err)
	assert.Equal(t, "", file)
}

// maybeBackupDB should write the number of attempts to the trigger file if the
// backup fails.
func TestMaybeBackupDBFail(t *testing.T) {
	testutils.SmallTest(t)
	now := time.Date(2016, 10, 26, 5, 0, 0, 0, time.UTC)
	r := mux.NewRouter()

	gsRoute(r).Methods("POST").Path(fmt.Sprintf("/upload/storage/v1/b/%s/o", TEST_BUCKET)).
		Queries("uploadType", "multipart").
		HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			util.Close(r.Body)
			http.Error(w, "I don't like your poem.", http.StatusTeapot)
		})

	b, cancel := getMockedDBBackup(t, r)
	defer cancel()

	assert.NoError(t, b.writeTriggerFile("task-scheduler", 0))

	b.maybeBackupDB(now)

	file, attempts, err := b.findAndParseTriggerFile()
	assert.NoError(t, err)
	assert.Equal(t, "task-scheduler", file)
	assert.Equal(t, 1, attempts)
}

// maybeBackupDB should delete the trigger file if retries are exhausted.
func TestMaybeBackupDBRetriesExhausted(t *testing.T) {
	testutils.SmallTest(t)
	now := time.Date(2016, 10, 26, 5, 0, 0, 0, time.UTC)
	r := mux.NewRouter()

	gsRoute(r).Methods("POST").Path(fmt.Sprintf("/upload/storage/v1/b/%s/o", TEST_BUCKET)).
		Queries("uploadType", "multipart").
		HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			util.Close(r.Body)
			http.Error(w, "I don't like your poem.", http.StatusTeapot)
		})

	b, cancel := getMockedDBBackup(t, r)
	defer cancel()

	assert.NoError(t, b.writeTriggerFile("task-scheduler", 2))

	b.maybeBackupDB(now)

	file, _, err := b.findAndParseTriggerFile()
	assert.NoError(t, err)
	assert.Equal(t, "", file)
}
