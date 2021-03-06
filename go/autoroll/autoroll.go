package autoroll

/*
	Convenience functions for retrieving AutoRoll CLs.
*/

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"time"

	"go.skia.org/infra/go/buildbucket"
	"go.skia.org/infra/go/rietveld"
	"go.skia.org/infra/go/util"
)

const (
	AUTOROLL_STATUS_URL = "https://autoroll.skia.org/json/status"
	ROLL_AUTHOR         = "skia-deps-roller@chromium.org"
	POLLER_ROLLS_LIMIT  = 10
	RECENT_ROLLS_LIMIT  = 200
	RIETVELD_URL        = "https://codereview.chromium.org"

	ROLL_RESULT_DRY_RUN_SUCCESS     = "dry run succeeded"
	ROLL_RESULT_DRY_RUN_FAILURE     = "dry run failed"
	ROLL_RESULT_DRY_RUN_IN_PROGRESS = "dry run in progress"
	ROLL_RESULT_IN_PROGRESS         = "in progress"
	ROLL_RESULT_SUCCESS             = "succeeded"
	ROLL_RESULT_FAILURE             = "failed"

	TRYBOT_CATEGORY_CQ = "cq"

	TRYBOT_STATUS_STARTED   = "STARTED"
	TRYBOT_STATUS_COMPLETED = "COMPLETED"
	TRYBOT_STATUS_SCHEDULED = "SCHEDULED"

	TRYBOT_RESULT_CANCELED = "CANCELED"
	TRYBOT_RESULT_SUCCESS  = "SUCCESS"
	TRYBOT_RESULT_FAILURE  = "FAILURE"
)

var (
	ROLL_REV_REGEX = regexp.MustCompile("Roll .+ ([0-9a-zA-Z]+)\\.\\.([0-9a-zA-Z]+) \\(\\d+ commit.*\\)\\.")

	OPEN_ROLL_VALID_RESULTS = []string{
		ROLL_RESULT_DRY_RUN_FAILURE,
		ROLL_RESULT_DRY_RUN_IN_PROGRESS,
		ROLL_RESULT_DRY_RUN_SUCCESS,
		ROLL_RESULT_IN_PROGRESS,
	}

	DRY_RUN_RESULTS = []string{
		ROLL_RESULT_DRY_RUN_FAILURE,
		ROLL_RESULT_DRY_RUN_IN_PROGRESS,
		ROLL_RESULT_DRY_RUN_SUCCESS,
	}

	FAILURE_RESULTS = []string{
		ROLL_RESULT_DRY_RUN_FAILURE,
		ROLL_RESULT_FAILURE,
	}

	SUCCESS_RESULTS = []string{
		ROLL_RESULT_DRY_RUN_SUCCESS,
		ROLL_RESULT_SUCCESS,
	}
)

// AutoRollIssue is a trimmed-down rietveld.Issue containing just the
// fields we care about for AutoRoll CLs.
type AutoRollIssue struct {
	Closed            bool         `json:"closed"`
	Committed         bool         `json:"committed"`
	CommitQueue       bool         `json:"commitQueue"`
	CommitQueueDryRun bool         `json:"cqDryRun"`
	Created           time.Time    `json:"created"`
	Issue             int64        `json:"issue"`
	Modified          time.Time    `json:"modified"`
	Patchsets         []int64      `json:"patchSets"`
	Result            string       `json:"result"`
	RollingFrom       string       `json:"rollingFrom"`
	RollingTo         string       `json:"rollingTo"`
	Subject           string       `json:"subject"`
	TryResults        []*TryResult `json:"tryResults"`
}

// Validate returns an error iff there is some problem with the issue.
func (i *AutoRollIssue) Validate() error {
	if i.Closed {
		if i.Result == ROLL_RESULT_IN_PROGRESS {
			return fmt.Errorf("AutoRollIssue cannot have a Result of %q if it is Closed.", ROLL_RESULT_IN_PROGRESS)
		}
		if i.CommitQueue {
			return errors.New("AutoRollIssue cannot be marked CommitQueue if it is Closed.")
		}
	} else {
		if i.Committed {
			return errors.New("AutoRollIssue cannot be Committed without being Closed.")
		}
		if !util.In(i.Result, OPEN_ROLL_VALID_RESULTS) {
			return fmt.Errorf("AutoRollIssue which is not Closed must have as a Result one of: %v", OPEN_ROLL_VALID_RESULTS)
		}
	}
	return nil
}

// Copy returns a copy of the AutoRollIssue.
func (i *AutoRollIssue) Copy() *AutoRollIssue {
	var patchsetsCpy []int64
	if i.Patchsets != nil {
		patchsetsCpy = make([]int64, len(i.Patchsets))
		copy(patchsetsCpy, i.Patchsets)
	}
	var tryResultsCpy []*TryResult
	if i.TryResults != nil {
		tryResultsCpy = make([]*TryResult, 0, len(i.TryResults))
		for _, t := range i.TryResults {
			tryResultsCpy = append(tryResultsCpy, t.Copy())
		}
	}
	return &AutoRollIssue{
		Closed:            i.Closed,
		Committed:         i.Committed,
		CommitQueue:       i.CommitQueue,
		CommitQueueDryRun: i.CommitQueueDryRun,
		Created:           i.Created,
		Issue:             i.Issue,
		Modified:          i.Modified,
		Patchsets:         patchsetsCpy,
		Result:            i.Result,
		RollingFrom:       i.RollingFrom,
		RollingTo:         i.RollingTo,
		Subject:           i.Subject,
		TryResults:        tryResultsCpy,
	}
}

// FromRietveldIssue returns an AutoRollIssue instance based on the given
// rietveld.Issue.
func FromRietveldIssue(i *rietveld.Issue, fullHashFn func(string) (string, error)) (*AutoRollIssue, error) {
	roll := &AutoRollIssue{
		Closed:            i.Closed,
		Committed:         i.Committed,
		CommitQueue:       i.CommitQueue,
		CommitQueueDryRun: i.CommitQueueDryRun,
		Created:           i.Created,
		Issue:             i.Issue,
		Modified:          i.Modified,
		Patchsets:         i.Patchsets,
		Subject:           i.Subject,
	}
	roll.Result = rollResult(roll)
	from, to, err := rollRev(roll.Subject, fullHashFn)
	if err != nil {
		return nil, err
	}
	roll.RollingFrom = from
	roll.RollingTo = to
	return roll, nil
}

// rollResult derives a result string for the roll.
func rollResult(roll *AutoRollIssue) string {
	if roll.Closed {
		if roll.Committed {
			return ROLL_RESULT_SUCCESS
		} else {
			return ROLL_RESULT_FAILURE
		}
	}
	return ROLL_RESULT_IN_PROGRESS
}

// rollRev returns the commit the given roll is rolling from and to.
func rollRev(subject string, fullHashFn func(string) (string, error)) (string, string, error) {
	matches := ROLL_REV_REGEX.FindStringSubmatch(subject)
	if matches == nil {
		return "", "", fmt.Errorf("No roll revision found in %q", subject)
	}
	if len(matches) != 3 {
		return "", "", fmt.Errorf("Unable to parse revisions from issue subject: %q", subject)
	}
	if fullHashFn == nil {
		return matches[1], matches[2], nil
	}
	from, err := fullHashFn(matches[1])
	if err != nil {
		return "", "", err
	}
	to, err := fullHashFn(matches[2])
	if err != nil {
		return "", "", err
	}
	return from, to, nil
}

// AllTrybotsFinished returns true iff all known trybots have finished for the
// given issue.
func (a *AutoRollIssue) AllTrybotsFinished() bool {
	for _, t := range a.TryResults {
		if !t.Finished() {
			return false
		}
	}
	return true
}

// AllTrybotsSucceeded returns true iff all known trybots have succeeded for the
// given issue. Note that some trybots may fail and be retried, in which case a
// successful retry counts as a success.
func (a *AutoRollIssue) AllTrybotsSucceeded() bool {
	// For each trybot, find the most recent result.
	bots := map[string]*TryResult{}
	for _, t := range a.TryResults {
		if prev, ok := bots[t.Builder]; !ok || prev.Created.Before(t.Created) {
			bots[t.Builder] = t
		}
	}
	for _, t := range bots {
		if t.Category != TRYBOT_CATEGORY_CQ {
			continue
		}
		if !t.Succeeded() {
			return false
		}
	}
	return true
}

// Failed returns true iff the roll failed (including dry run failure).
func (a *AutoRollIssue) Failed() bool {
	return util.In(a.Result, FAILURE_RESULTS)
}

// Succeeded returns true iff the roll succeeded (including dry run success).
func (a *AutoRollIssue) Succeeded() bool {
	return util.In(a.Result, SUCCESS_RESULTS)
}

// TryResult is a struct which contains trybot result details.
type TryResult struct {
	Builder  string    `json:"builder"`
	Category string    `json:"category"`
	Created  time.Time `json:"created_ts"`
	Result   string    `json:"result"`
	Status   string    `json:"status"`
	Url      string    `json:"url"`
}

// TryResultFromBuildbucket returns a new TryResult based on a buildbucket.Build.
func TryResultFromBuildbucket(b *buildbucket.Build) (*TryResult, error) {
	var params struct {
		Builder  string `json:"builder_name"`
		Category string `json:"category"`
	}
	if err := json.Unmarshal([]byte(b.ParametersJson), &params); err != nil {
		return nil, err
	}
	return &TryResult{
		Builder:  params.Builder,
		Category: params.Category,
		Created:  time.Time(b.Created),
		Result:   b.Result,
		Status:   b.Status,
		Url:      b.Url,
	}, nil
}

// Finished returns true iff the trybot is done running.
func (t TryResult) Finished() bool {
	return t.Status == TRYBOT_STATUS_COMPLETED
}

// Succeeded returns true iff the trybot completed successfully.
func (t TryResult) Succeeded() bool {
	return t.Finished() && t.Result == TRYBOT_RESULT_SUCCESS
}

// Copy returns a copy of the TryResult.
func (t *TryResult) Copy() *TryResult {
	return &TryResult{
		Builder:  t.Builder,
		Category: t.Category,
		Created:  t.Created,
		Result:   t.Result,
		Status:   t.Status,
		Url:      t.Url,
	}
}

type autoRollIssueSlice []*AutoRollIssue

func (s autoRollIssueSlice) Len() int           { return len(s) }
func (s autoRollIssueSlice) Less(i, j int) bool { return s[i].Modified.After(s[j].Modified) }
func (s autoRollIssueSlice) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

type tryResultSlice []*TryResult

func (s tryResultSlice) Len() int           { return len(s) }
func (s tryResultSlice) Less(i, j int) bool { return s[i].Builder < s[j].Builder }
func (s tryResultSlice) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

// search queries Rietveld for issues matching the known DEPS roll format.
func search(r *rietveld.Rietveld, limit int, fullHashFn func(string) (string, error), terms ...*rietveld.SearchTerm) ([]*AutoRollIssue, error) {
	terms = append(terms, rietveld.SearchOwner(ROLL_AUTHOR))
	res, err := r.Search(limit, terms...)
	if err != nil {
		return nil, err
	}
	rv := make([]*AutoRollIssue, 0, len(res))
	for _, i := range res {
		if ROLL_REV_REGEX.FindString(i.Subject) != "" {
			ari, err := FromRietveldIssue(i, fullHashFn)
			if err != nil {
				return nil, err
			}
			rv = append(rv, ari)
		}
	}
	sort.Sort(autoRollIssueSlice(rv))
	return rv, nil
}

// GetRecentRolls returns any DEPS rolls modified after the given Time, with a
// limit of RECENT_ROLLS_LIMIT.
func GetRecentRolls(r *rietveld.Rietveld, modifiedAfter time.Time, fullHashFn func(string) (string, error)) ([]*AutoRollIssue, error) {
	issues, err := search(r, RECENT_ROLLS_LIMIT, fullHashFn, rietveld.SearchModifiedAfter(modifiedAfter))
	if err != nil {
		return nil, err
	}
	return issues, nil
}

// GetTryResults returns trybot results for the given roll.
func GetTryResults(r *rietveld.Rietveld, roll *AutoRollIssue) ([]*TryResult, error) {
	tries, err := r.GetTrybotResults(roll.Issue, roll.Patchsets[len(roll.Patchsets)-1])
	if err != nil {
		return nil, err
	}
	res := make([]*TryResult, 0, len(tries))
	for _, t := range tries {
		tryResult, err := TryResultFromBuildbucket(t)
		if err != nil {
			return nil, err
		}
		res = append(res, tryResult)
	}
	sort.Sort(tryResultSlice(res))
	return res, nil
}
