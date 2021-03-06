// Google Storage utility that contains methods for both CT master and worker
// scripts.
package util

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/skia-dev/glog"
	"go.skia.org/infra/go/auth"
	"go.skia.org/infra/go/gs"
	"go.skia.org/infra/go/util"
	googleapi "google.golang.org/api/googleapi"
	storage "google.golang.org/api/storage/v1"
)

const (
	DOWNLOAD_UPLOAD_GOROUTINE_POOL_SIZE = 30
	// Use larger pool size for deletions. This is useful when deleting directories
	// with 1M/1B subdirectories from the master. Google Storage will not be overwhelmed
	// because all slaves do not do large scale deletions at the same time.
	DELETE_GOROUTINE_POOL_SIZE = 1000
)

type GsUtil struct {
	// The client used to connect to Google Storage.
	client  *http.Client
	service *storage.Service
}

// NewGsUtil initializes and returns a utility for CT interations with Google
// Storage. If client is nil then auth.NewClient is invoked.
func NewGsUtil(client *http.Client) (*GsUtil, error) {
	if client == nil {
		var err error
		client, err = auth.NewClientWithTransport(true, GSTokenPath, ClientSecretPath, nil, auth.SCOPE_FULL_CONTROL)
		if err != nil {
			return nil, err
		}
	}
	client.Timeout = HTTP_CLIENT_TIMEOUT
	service, err := storage.New(client)
	if err != nil {
		return nil, fmt.Errorf("Failed to create interface to Google Storage: %s", err)
	}
	return &GsUtil{client: client, service: service}, nil
}

// Returns the response body of the specified GS object. Tries MAX_URI_GET_TRIES
// times if download is unsuccessful. Client must close the response body when
// finished with it.
func getRespBody(res *storage.Object, client *http.Client) (io.ReadCloser, error) {
	for i := 0; i < MAX_URI_GET_TRIES; i++ {
		glog.Infof("Fetching: %s", res.Name)
		request, err := gs.RequestForStorageURL(res.MediaLink)
		if err != nil {
			glog.Warningf("Unable to create Storage MediaURI request: %s\n", err)
			continue
		}

		resp, err := client.Do(request)
		if err != nil {
			glog.Warningf("Unable to retrieve Storage MediaURI: %s", err)
			continue
		}
		if resp.StatusCode != 200 {
			glog.Warningf("Failed to retrieve: %d  %s", resp.StatusCode, resp.Status)
			util.Close(resp.Body)
			continue
		}
		return resp.Body, nil
	}
	return nil, fmt.Errorf("Failed fetching file after %d attempts", MAX_URI_GET_TRIES)
}

// Returns the response body of the specified GS file. Client must close the
// response body when finished with it.
func (gs *GsUtil) GetRemoteFileContents(filePath string) (io.ReadCloser, error) {
	res, err := gs.service.Objects.Get(GSBucketName, filePath).Do()
	if err != nil {
		return nil, fmt.Errorf("Could not get %s from GS: %s", filePath, err)
	}
	return getRespBody(res, gs.client)
}

type filePathToStorageObject struct {
	storageObject *storage.Object
	filePath      string
}

// downloadRemoteDir downloads the specified Google Storage dir to the specified
// local dir. The local dir will be emptied and recreated. Handles multiple levels
// of directories.
func (gs *GsUtil) downloadRemoteDir(localDir, gsDir string) error {
	// Empty the local dir.
	util.RemoveAll(localDir)
	// Create the local dir.
	util.MkdirAll(localDir, 0700)
	// The channel where the storage objects to be downloaded will be sent to.
	chStorageObjects := make(chan filePathToStorageObject, DOWNLOAD_UPLOAD_GOROUTINE_POOL_SIZE)

	// Kick off one goroutine to populate the channel.
	errPopulator := make(chan error, 1)
	var wgPopulator sync.WaitGroup
	wgPopulator.Add(1)
	go func() {
		defer wgPopulator.Done()
		defer close(chStorageObjects)
		req := gs.service.Objects.List(GSBucketName).Prefix(gsDir + "/")
		for req != nil {
			resp, err := req.Do()
			if err != nil {
				errPopulator <- fmt.Errorf("Error occured while listing %s: %s", gsDir, err)
				return
			}
			for _, result := range resp.Items {
				fileName := filepath.Base(result.Name)
				// If downloading from subdir then add it to the fileName.
				fileGsDir := filepath.Dir(result.Name)
				subDirs := strings.TrimPrefix(fileGsDir, gsDir)
				if subDirs != "" {
					dirTokens := strings.Split(subDirs, "/")
					for i := range dirTokens {
						fileName = filepath.Join(dirTokens[len(dirTokens)-i-1], fileName)
					}
					// Create the local directory.
					util.MkdirAll(filepath.Join(localDir, filepath.Dir(fileName)), 0700)
				}
				chStorageObjects <- filePathToStorageObject{storageObject: result, filePath: fileName}
			}
			if len(resp.NextPageToken) > 0 {
				req.PageToken(resp.NextPageToken)
			} else {
				req = nil
			}
		}
	}()

	// Kick off goroutines to download the storage objects.
	var wgConsumer sync.WaitGroup
	for i := 0; i < DOWNLOAD_UPLOAD_GOROUTINE_POOL_SIZE; i++ {
		wgConsumer.Add(1)
		go func(goroutineNum int) {
			defer wgConsumer.Done()
			for obj := range chStorageObjects {
				if err := downloadStorageObj(obj, gs.client, localDir, goroutineNum); err != nil {
					glog.Errorf("Could not download storage object: %s", err)
					return
				}
				// Sleep for a second after downloading file to avoid bombarding Cloud
				// storage.
				time.Sleep(time.Second)
			}
		}(i + 1)
	}

	wgPopulator.Wait()
	wgConsumer.Wait()
	// Check if there was an error listing the GS dir.
	select {
	case err, ok := <-errPopulator:
		if ok {
			return err
		}
	default:
	}
	return nil
}

func downloadStorageObj(obj filePathToStorageObject, c *http.Client, localDir string, goroutineNum int) error {
	result := obj.storageObject
	filePath := obj.filePath
	respBody, err := getRespBody(result, c)
	if err != nil {
		return fmt.Errorf("Could not fetch %s: %s", result.MediaLink, err)
	}
	defer util.Close(respBody)
	outputFile := filepath.Join(localDir, filePath)
	out, err := os.Create(outputFile)
	if err != nil {
		return fmt.Errorf("Unable to create file %s: %s", outputFile, err)
	}
	defer util.Close(out)
	if _, err = io.Copy(out, respBody); err != nil {
		return err
	}
	glog.Infof("Downloaded gs://%s/%s to %s with goroutine#%d", GSBucketName, result.Name, outputFile, goroutineNum)
	return nil
}

// DownloadChromiumBuild downloads the specified Chromium build from Google
// Storage to a local dir.
func (gs *GsUtil) DownloadChromiumBuild(chromiumBuild string) error {
	localDir := filepath.Join(ChromiumBuildsDir, chromiumBuild)
	gsDir := filepath.Join(CHROMIUM_BUILDS_DIR_NAME, chromiumBuild)
	glog.Infof("Downloading %s from Google Storage to %s", gsDir, localDir)
	if err := gs.downloadRemoteDir(localDir, gsDir); err != nil {
		return fmt.Errorf("Error downloading %s into %s: %s", gsDir, localDir, err)
	}
	// Downloaded chrome binary needs to be set as an executable.
	util.LogErr(os.Chmod(filepath.Join(localDir, "chrome"), 0777))

	return nil
}

func (gs *GsUtil) DeleteRemoteDir(gsDir string) error {
	// The channel where the GS filepaths to be deleted will be sent to.
	chFilePaths := make(chan string, DELETE_GOROUTINE_POOL_SIZE)

	// Kick off one goroutine to populate the channel.
	errPopulator := make(chan error, 1)
	var wgPopulator sync.WaitGroup
	wgPopulator.Add(1)
	go func() {
		defer wgPopulator.Done()
		defer close(chFilePaths)
		req := gs.service.Objects.List(GSBucketName).Prefix(gsDir + "/")
		for req != nil {
			resp, err := req.Do()
			if err != nil {
				errPopulator <- fmt.Errorf("Error occured while listing %s: %s", gsDir, err)
				return
			}
			for _, result := range resp.Items {
				chFilePaths <- result.Name
			}
			if len(resp.NextPageToken) > 0 {
				req.PageToken(resp.NextPageToken)
			} else {
				req = nil
			}
		}
	}()

	// Kick off goroutines to delete the file paths.
	var wgConsumer sync.WaitGroup
	for i := 0; i < DELETE_GOROUTINE_POOL_SIZE; i++ {
		wgConsumer.Add(1)
		go func(goroutineNum int) {
			defer wgConsumer.Done()
			for filePath := range chFilePaths {
				if err := gs.service.Objects.Delete(GSBucketName, filePath).Do(); err != nil {
					glog.Errorf("Goroutine#%d could not delete %s: %s", goroutineNum, filePath, err)
					return
				}
				glog.Infof("Deleted gs://%s/%s with goroutine#%d", GSBucketName, filePath, goroutineNum)
				// Sleep for a second after deleting file to avoid bombarding Cloud
				// storage.
				time.Sleep(time.Second)
			}
		}(i + 1)
	}

	wgPopulator.Wait()
	wgConsumer.Wait()
	// Check if there was an error listing the GS dir.
	select {
	case err, ok := <-errPopulator:
		if ok {
			return err
		}
	default:
	}
	return nil
}

// UploadFile uploads the specified file to the remote dir in Google Storage. It
// also sets the appropriate ACLs on the uploaded file.
func (gs *GsUtil) UploadFile(fileName, localDir, gsDir string) error {
	localFile := filepath.Join(localDir, fileName)
	gsFile := filepath.Join(gsDir, fileName)
	object := &storage.Object{Name: gsFile}
	f, err := os.Open(localFile)
	if err != nil {
		return fmt.Errorf("Error opening %s: %s", localFile, err)
	}
	defer util.Close(f)
	// TODO(rmistry): gs api now enables resumable uploads by default. Handle 308
	// response codes.
	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("Error stating %s: %s", localFile, err)
	}
	mediaOption := googleapi.ChunkSize(int(fi.Size()))
	if _, err := gs.service.Objects.Insert(GSBucketName, object).Media(f, mediaOption).Do(); err != nil {
		return fmt.Errorf("Objects.Insert failed: %s", err)
	}
	glog.Infof("Copied %s to %s", localFile, fmt.Sprintf("gs://%s/%s", GSBucketName, gsFile))

	// All objects uploaded to CT's bucket via this util must be readable by
	// the google.com domain. This will be fine tuned later if required.
	objectAcl := &storage.ObjectAccessControl{
		Bucket: GSBucketName, Entity: "domain-google.com", Object: gsFile, Role: "READER",
	}
	if _, err := gs.service.ObjectAccessControls.Insert(GSBucketName, gsFile, objectAcl).Do(); err != nil {
		return fmt.Errorf("Could not update ACL of %s: %s", object.Name, err)
	}
	glog.Infof("Updated ACL of %s", fmt.Sprintf("gs://%s/%s", GSBucketName, gsFile))

	return nil
}

// UploadSwarmingArtifact uploads the specified local artifacts to Google Storage.
func (gs *GsUtil) UploadSwarmingArtifacts(dirName, pagesetType string) error {
	localDir := path.Join(StorageDir, dirName, pagesetType)
	gsDir := path.Join(SWARMING_DIR_NAME, dirName, pagesetType)

	return gs.UploadDir(localDir, gsDir, false)
}

// DownloadSwarmingArtifacts downloads the specified artifacts from Google Storage to a local dir.
// The Google storage directory is assumed to have numerical subdirs Eg: {1..1000}. This function
// downloads the contents of those directories into a local directory without the numerical
// subdirs.
// Returns the ranking/index of the downloaded artifact.
func (gs *GsUtil) DownloadSwarmingArtifacts(localDir, remoteDirName, pagesetType string, startRange, num int) (map[string]int, error) {
	// Empty the local dir.
	util.RemoveAll(localDir)
	// Create the local dir.
	util.MkdirAll(localDir, 0700)

	gsDir := filepath.Join(SWARMING_DIR_NAME, remoteDirName, pagesetType)
	endRange := num + startRange - 1
	// The channel where remote files to be downloaded will be sent to.
	chRemoteDirs := make(chan string, num)
	for i := startRange; i <= endRange; i++ {
		chRemoteDirs <- filepath.Join(gsDir, strconv.Itoa(i))
	}
	close(chRemoteDirs)

	// Dictionary of artifacts to its rank/index.
	artifactToIndex := map[string]int{}
	// Mutex to control access to the above dictionary.
	var mtx sync.Mutex
	// Kick off goroutines to download artifacts and populate the artifactToIndex dictionary.
	var wg sync.WaitGroup
	for i := 0; i < DOWNLOAD_UPLOAD_GOROUTINE_POOL_SIZE; i++ {
		wg.Add(1)
		go func(goroutineNum int) {
			defer wg.Done()
			for remoteDir := range chRemoteDirs {
				if err := gs.downloadFromSwarmingDir(remoteDir, gsDir, localDir, goroutineNum, &mtx, artifactToIndex); err != nil {
					glog.Error(err)
					return
				}
			}
		}(i + 1)
	}
	wg.Wait()
	if len(chRemoteDirs) != 0 {
		return artifactToIndex, fmt.Errorf("Unable to download all artifacts.")
	}
	return artifactToIndex, nil
}

// GetRemoteDirCount returns the number of objects in the specified dir.
func (gs *GsUtil) GetRemoteDirCount(gsDir string) (int, error) {
	req := gs.service.Objects.List(GSBucketName).Prefix(gsDir + "/")
	count := 0
	for req != nil {
		resp, err := req.Do()
		if err != nil {
			return -1, fmt.Errorf("Error occured while listing %s: %s", gsDir, err)
		}
		count += len(resp.Items)
		if len(resp.NextPageToken) > 0 {
			req.PageToken(resp.NextPageToken)
		} else {
			req = nil
		}
	}
	return count, nil
}

func (gs *GsUtil) downloadFromSwarmingDir(remoteDir, gsDir, localDir string, runID int, mtx *sync.Mutex, artifactToIndex map[string]int) error {
	req := gs.service.Objects.List(GSBucketName).Prefix(remoteDir + "/")
	for req != nil {
		resp, err := req.Do()
		if err != nil {
			return fmt.Errorf("Error occured while listing %s: %s", gsDir, err)
		}
		for _, result := range resp.Items {
			fileName := filepath.Base(result.Name)
			fileGsDir := filepath.Dir(result.Name)
			index, err := strconv.Atoi(path.Base(fileGsDir))
			if err != nil {
				return fmt.Errorf("%s was not in expected format: %s", fileGsDir, err)
			}
			respBody, err := getRespBody(result, gs.client)
			if err != nil {
				return fmt.Errorf("Could not fetch %s: %s", result.MediaLink, err)
			}
			defer util.Close(respBody)
			outputFile := filepath.Join(localDir, fileName)
			out, err := os.Create(outputFile)
			if err != nil {
				return fmt.Errorf("Unable to create file %s: %s", outputFile, err)
			}
			defer util.Close(out)
			if _, err = io.Copy(out, respBody); err != nil {
				return err
			}
			glog.Infof("Downloaded gs://%s/%s to %s with id#%d", GSBucketName, result.Name, outputFile, runID)
			// Sleep for a second after downloading file to avoid bombarding Cloud
			// storage.
			time.Sleep(time.Second)
			mtx.Lock()
			artifactToIndex[path.Join(localDir, fileName)] = index
			mtx.Unlock()
		}
		if len(resp.NextPageToken) > 0 {
			req.PageToken(resp.NextPageToken)
		} else {
			req = nil
		}
	}
	return nil
}

// UploadDir uploads the specified local dir into the specified Google Storage dir.
func (gs *GsUtil) UploadDir(localDir, gsDir string, cleanDir bool) error {
	if cleanDir {
		// Empty the remote dir before uploading to it.
		util.LogErr(gs.DeleteRemoteDir(gsDir))
	}

	// Construct a dictionary of file paths to their file infos.
	pathsToFileInfos := map[string]os.FileInfo{}
	visit := func(path string, f os.FileInfo, err error) error {
		if f.IsDir() {
			return nil
		}
		pathsToFileInfos[path] = f
		return nil
	}
	if err := filepath.Walk(localDir, visit); err != nil {
		return fmt.Errorf("Unable to read the local dir %s: %s", localDir, err)
	}

	// The channel where the filepaths to be uploaded will be sent to.
	chFilePaths := make(chan string, len(pathsToFileInfos))
	// File filepaths and send it to the above channel.
	for path, fileInfo := range pathsToFileInfos {
		fileName := fileInfo.Name()
		containingDir := strings.TrimSuffix(path, fileName)
		subDirs := strings.TrimPrefix(containingDir, localDir)
		if subDirs != "" {
			dirTokens := strings.Split(subDirs, "/")
			for i := range dirTokens {
				fileName = filepath.Join(dirTokens[len(dirTokens)-i-1], fileName)
			}
		}
		chFilePaths <- fileName
	}
	close(chFilePaths)

	// Kick off goroutines to upload the file paths.
	var wg sync.WaitGroup
	for i := 0; i < DOWNLOAD_UPLOAD_GOROUTINE_POOL_SIZE; i++ {
		wg.Add(1)
		go func(goroutineNum int) {
			defer wg.Done()
			for filePath := range chFilePaths {
				glog.Infof("Uploading %s to %s with goroutine#%d", filePath, gsDir, goroutineNum)
				if err := gs.UploadFile(filePath, localDir, gsDir); err != nil {
					glog.Errorf("Goroutine#%d could not upload %s to %s: %s", goroutineNum, filePath, localDir, err)
				}
				// Sleep for a second after uploading file to avoid bombarding Cloud
				// storage.
				time.Sleep(time.Second)
			}
		}(i + 1)
	}
	wg.Wait()
	return nil
}

// DownloadRemoteFile downloads the specified remote path into the specified local file.
// This function has been tested to download very large files (~33GB).
// TODO(rmistry): Update all code that downloads remote files to use this method.
func (gs *GsUtil) DownloadRemoteFile(remotePath, localPath string) error {
	respBody, err := gs.GetRemoteFileContents(remotePath)
	if err != nil {
		return err
	}
	defer util.Close(respBody)
	out, err := os.Create(localPath)
	if err != nil {
		return err
	}

	bufferSize := int64(1024 * 1024 * 1024)
	for {
		_, err := io.CopyN(out, respBody, bufferSize)
		if err == io.EOF {
			break
		} else if err != nil {
			defer util.Close(out)
			return err
		}
		// Sleep for 30 seconds. Bots run out of memory without this.
		// Eg: https://chromium-swarm.appspot.com/task?id=2fba9fba3d553510
		// Maybe this sleep gives Golang time to clear some caches.
		time.Sleep(30 * time.Second)
	}
	if err := out.Close(); err != nil {
		return err
	}
	return nil
}
