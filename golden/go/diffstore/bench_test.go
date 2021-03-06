package diffstore

import (
	"os"
	"sync"
	"testing"

	assert "github.com/stretchr/testify/require"

	"go.skia.org/infra/go/testutils"
	"go.skia.org/infra/go/util"
	"go.skia.org/infra/golden/go/diff"
	"go.skia.org/infra/golden/go/ignore"
	"go.skia.org/infra/golden/go/serialize"
	"go.skia.org/infra/golden/go/types"
)

const (
	TEST_FILE_NAME  = "sample.tile"
	PROCESS_N_TESTS = 5000
)

func BenchmarkMemDiffStore(b *testing.B) {
	sample := loadSample(b)

	baseDir := TEST_DATA_BASE_DIR + "-bench-diffstore"
	client := getClient(b)
	defer testutils.RemoveAll(b, baseDir)

	memIgnoreStore := ignore.NewMemIgnoreStore()
	for _, ir := range sample.IgnoreRules {
		assert.NoError(b, memIgnoreStore.Create(ir))
	}
	ignoreMatcher, err := memIgnoreStore.BuildRuleMatcher()
	assert.NoError(b, err)

	// Build storages and get the digests that are not ignored.
	byTest := map[string]util.StringSet{}
	for _, trace := range sample.Tile.Traces {
		gTrace := trace.(*types.GoldenTrace)
		if _, ok := ignoreMatcher(gTrace.Params_); !ok && gTrace.Params_[types.CORPUS_FIELD] == "gm" {
			testName := gTrace.Params_[types.PRIMARY_KEY_FIELD]
			if found, ok := byTest[testName]; ok {
				found.AddLists(gTrace.Values)
			} else {
				byTest[testName] = util.NewStringSet(gTrace.Values)
			}
		}
	}

	diffStore, err := New(client, baseDir, []string{TEST_GS_BUCKET_NAME}, TEST_GS_IMAGE_DIR, 10)
	allDigests := make([][]string, 0, PROCESS_N_TESTS)
	processed := 0
	var wg sync.WaitGroup
	for _, digestSet := range byTest {
		// Remove the missing digest sentinel.
		delete(digestSet, types.MISSING_DIGEST)

		digests := digestSet.Keys()
		allDigests = append(allDigests, digests)
		diffStore.WarmDigests(diff.PRIORITY_NOW, digests)

		wg.Add(1)
		go func(digests []string) {
			defer wg.Done()
			for _, d1 := range digests {
				_, _ = diffStore.Get(diff.PRIORITY_NOW, d1, digests)
			}
		}(digests)

		processed++
		if processed >= PROCESS_N_TESTS {
			break
		}
	}
	wg.Wait()

	// Now retrieve all of them again.
	b.ResetTimer()
	for _, digests := range allDigests {
		wg.Add(1)
		go func(digests []string) {
			defer wg.Done()
			for _, d1 := range digests {
				_, _ = diffStore.Get(diff.PRIORITY_NOW, d1, digests)
			}
		}(digests)
	}
	wg.Wait()
}

func loadSample(t assert.TestingT) *serialize.Sample {
	file, err := os.Open(TEST_FILE_NAME)
	assert.NoError(t, err)

	sample, err := serialize.DeserializeSample(file)
	assert.NoError(t, err)

	return sample
}
