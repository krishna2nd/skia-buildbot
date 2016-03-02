package swarming

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	assert "github.com/stretchr/testify/require"
	"go.skia.org/infra/go/testutils"
)

const (
	TESTDATA_DIR = "testdata"
	TEST_ISOLATE = "test.isolate"
	TEST_SCRIPT  = "test.py"
)

// TestCreateIsolatedGenJSON verifies that an isolated.gen.json with expected
// values is created from the test isolated files.
func TestCreateIsolatedGenJSON(t *testing.T) {
	workDir, err := ioutil.TempDir("", "swarming_work_")
	assert.Nil(t, err)
	s, err := NewSwarmingClient(workDir)
	assert.Nil(t, err)
	defer s.Cleanup()

	extraArgs := map[string]string{
		"ARG_1": "arg_1",
		"ARG_2": "arg_2",
	}
	blackList := []string{"blacklist1", "blacklist2"}

	// Pass in a relative path to isolate file. It should return an err.
	genJSON, err := s.CreateIsolatedGenJSON(path.Join(TESTDATA_DIR, TEST_ISOLATE), TESTDATA_DIR, "linux", "testTask1", extraArgs, blackList)
	assert.Equal(t, "", genJSON)
	assert.NotNil(t, err)
	assert.Equal(t, "isolate path testdata/test.isolate must be an absolute path", err.Error())

	// Now pass in an absolute path to isolate file. This should succeed.
	absTestDataDir, err := filepath.Abs(TESTDATA_DIR)
	assert.Nil(t, err)
	genJSON, err = s.CreateIsolatedGenJSON(path.Join(absTestDataDir, TEST_ISOLATE), TESTDATA_DIR, "linux", "testTask1", extraArgs, blackList)
	assert.Nil(t, err)
	contents, err := ioutil.ReadFile(genJSON)
	assert.Nil(t, err)
	var output GenJSONFormat
	err = json.Unmarshal(contents, &output)
	assert.Nil(t, err)

	assert.Equal(t, 1, output.Version)
	assert.Equal(t, TESTDATA_DIR, output.Dir)
	// Assert the args value. The position of the extra vars is non-deterministic
	// because it is in a map.
	expectedOutputBeforeExtraVars := []string{
		"--isolate", path.Join(absTestDataDir, TEST_ISOLATE),
		"--isolated", fmt.Sprintf("%s/testTask1.isolated", s.WorkDir),
		"--config-variable", "OS", "linux",
		"--blacklist", "blacklist1", "--blacklist", "blacklist2"}
	extraVarsPos := len(output.Args) - 6
	assert.Equal(t, output.Args[:extraVarsPos], expectedOutputBeforeExtraVars)
	assert.Equal(t, 17, len(output.Args))
}

// E2E_Success verifies that an islated.gen.json is created, batcharchive works,
// triggering swarming tasks works and collecting swarming tasks works.
func E2E_Success(t *testing.T) {
	testutils.SkipIfShort(t)

	// Instantiate the swarming client.
	workDir, err := ioutil.TempDir("", "swarming_work_")
	assert.Nil(t, err)
	s, err := NewSwarmingClient(workDir)
	assert.Nil(t, err)
	defer s.Cleanup()

	// Create isolated.gen.json files to pass to batcharchive.
	blackList := []string{"blacklist1", "blacklist2"}
	absTestDataDir, err := filepath.Abs(TESTDATA_DIR)
	assert.Nil(t, err)
	taskNames := []string{"testTask1", "testTask2"}
	genJSONs := []string{}
	for _, taskName := range taskNames {
		extraArgs := map[string]string{
			"ARG_1": fmt.Sprintf("arg_1_%s", taskName),
			"ARG_2": fmt.Sprintf("arg_2_%s", taskName),
		}
		genJSON, err := s.CreateIsolatedGenJSON(path.Join(absTestDataDir, TEST_ISOLATE), s.WorkDir, "linux", taskName, extraArgs, blackList)
		assert.Nil(t, err)
		genJSONs = append(genJSONs, genJSON)
	}

	// Batcharchive the task.
	tasksToHashes, err := s.BatchArchiveTargets(genJSONs, 5*time.Minute)
	assert.Nil(t, err)
	assert.Equal(t, 2, len(tasksToHashes))
	for _, taskName := range taskNames {
		hash, exists := tasksToHashes[taskName]
		assert.True(t, exists)
		assert.NotNil(t, hash)
	}

	// Trigger swarming using the isolate hashes.
	dimensions := map[string]string{"pool": "Chrome"}
	tasks, err := s.TriggerSwarmingTasks(tasksToHashes, dimensions, RECOMMENDED_PRIORITY, RECOMMENDED_EXPIRATION, false)
	assert.Nil(t, err)

	// Collect both output and file output of all tasks.
	for _, task := range tasks {
		output, outputDir, err := task.Collect(s)
		assert.Nil(t, err)
		output = sanitizeOutput(output)
		assert.Equal(t, fmt.Sprintf("arg_1_%s\narg_2_%s\n", task.Title, task.Title), output)
		// Verify contents of the outputDir.
		rawFileOutput, err := ioutil.ReadFile(path.Join(outputDir, "output.txt"))
		assert.Nil(t, err)
		fileOutput := strings.Replace(string(rawFileOutput), "\r\n", "\n", -1)
		assert.Equal(t, "testing\ntesting", fileOutput)
	}
}

// E2E_OnFailure verifies that an islated.gen.json is created, batcharchive
// works, triggering swarming tasks works and collecting swarming tasks with one
// failure works.
func E2E_OneFailure(t *testing.T) {
	testutils.SkipIfShort(t)

	// Instantiate the swarming client.
	workDir, err := ioutil.TempDir("", "swarming_work_")
	assert.Nil(t, err)
	s, err := NewSwarmingClient(workDir)
	assert.Nil(t, err)
	defer s.Cleanup()

	// Create isolated.gen.json files to pass to batcharchive.
	blackList := []string{"blacklist1", "blacklist2"}
	absTestDataDir, err := filepath.Abs(TESTDATA_DIR)
	assert.Nil(t, err)
	taskNames := []string{"testTask1", "testTask2"}
	genJSONs := []string{}
	for _, taskName := range taskNames {
		extraArgs := map[string]string{
			"ARG_1": fmt.Sprintf("arg_1_%s", taskName),
			"ARG_2": fmt.Sprintf("arg_2_%s", taskName),
		}
		// Add an empty 2nd argument for testTask1 to cause a failure.
		if taskName == "testTask1" {
			extraArgs["ARG_2"] = ""
		}
		genJSON, err := s.CreateIsolatedGenJSON(path.Join(absTestDataDir, TEST_ISOLATE), s.WorkDir, "linux", taskName, extraArgs, blackList)
		assert.Nil(t, err)
		genJSONs = append(genJSONs, genJSON)
	}

	// Batcharchive the task.
	tasksToHashes, err := s.BatchArchiveTargets(genJSONs, 5*time.Minute)
	assert.Nil(t, err)
	assert.Equal(t, 2, len(tasksToHashes))
	for _, taskName := range taskNames {
		hash, exists := tasksToHashes[taskName]
		assert.True(t, exists)
		assert.NotNil(t, hash)
	}

	// Trigger swarming using the isolate hashes.
	dimensions := map[string]string{"pool": "Chrome"}
	tasks, err := s.TriggerSwarmingTasks(tasksToHashes, dimensions, RECOMMENDED_PRIORITY, RECOMMENDED_EXPIRATION, false)
	assert.Nil(t, err)

	// Collect testTask1. It should have failed.
	output1, outputDir1, err1 := tasks[0].Collect(s)
	output1 = sanitizeOutput(output1)
	assert.Equal(t, "", output1)
	assert.Equal(t, "", outputDir1)
	assert.NotNil(t, err1)
	assert.True(t, strings.HasPrefix(err1.Error(), "Swarming trigger for testTask1 failed with: Command exited with exit status 1: "))

	// Collect testTask2. It should have succeeded.
	output2, outputDir2, err2 := tasks[1].Collect(s)
	assert.Nil(t, err2)
	output2 = sanitizeOutput(output2)
	assert.Equal(t, fmt.Sprintf("arg_1_%s\narg_2_%s\n", tasks[1].Title, tasks[1].Title), output2)
	// Verify contents of the outputDir.
	rawFileOutput, err := ioutil.ReadFile(path.Join(outputDir2, "output.txt"))
	assert.Nil(t, err)
	fileOutput := strings.Replace(string(rawFileOutput), "\r\n", "\n", -1)
	assert.Equal(t, "testing\ntesting", fileOutput)
}

// sanitizeOutput makes the task output consistent. Sometimes the outputs comes
// back with "\r\n" and sometimes with "\n". This function makes it always be "\n".
func sanitizeOutput(output string) string {
	return strings.Replace(output, "\r\n", "\n", -1)
}
