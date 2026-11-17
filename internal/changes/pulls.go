package changes

import (
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/brettinternet/backlog/internal/cache"
)

// MaxPulls bounds the pull requests a forge keeps, the newest updated.
const MaxPulls = 1000

// PullProgress is how far a forge has read its pull requests, newest updated
// first. A refresh reads every one updated since Cursor. A read interrupted
// partway leaves Resume, the page cursor after the last page read, whose
// pull requests were updated at or before Oldest; the next refresh continues
// from it down to Since, the cursor the interrupted read began from.
type PullProgress struct {
	Cursor        time.Time
	Resume        string    `json:",omitempty"`
	Oldest, Since time.Time `json:",omitzero"`
}

type pullCache struct {
	Repo string
	PullProgress
	// Fetched is when the last successful refresh began.
	Fetched time.Time
	Pulls   []PullRequest
}

// RefreshPulls merges a forge's newest reads into its cache at path and
// returns the merged list, redacted before anything is kept or shown, as
// provider activity is. fetch reads every open pull request, whose checks
// change without touching its update time, and every other one updated since
// the cached progress, and returns the progress it reached. A cache refreshed
// within fresh is returned without a fetch, so readers sharing it add no
// request. A failed fetch keeps whatever it read, so a large first read
// converges over later refreshes, and returns the list with the error, so
// its entries read as stale.
func RefreshPulls(path, repo string, fresh time.Duration, redact func(string) string, fetch func(PullProgress) (open, updated []PullRequest, next PullProgress, err error)) ([]PullRequest, error) {
	var cached pullCache
	if data, err := os.ReadFile(path); err == nil && json.Unmarshal(data, &cached) == nil && cached.Repo != repo {
		cached = pullCache{}
	}
	started := time.Now()
	if cached.Repo == repo && started.Sub(cached.Fetched) < fresh {
		return redactPulls(cached, redact)
	}
	open, updated, next, fetchErr := fetch(cached.PullProgress)
	if fetchErr != nil && open == nil && updated == nil {
		return cached.Pulls, fetchErr
	}
	// The later read wins: a pull request merged between the two reads is
	// merged.
	seen := map[int]bool{}
	var merged []PullRequest
	for _, list := range [][]PullRequest{updated, open, cached.Pulls} {
		for _, pr := range list {
			if !seen[pr.Number] {
				seen[pr.Number] = true
				merged = append(merged, pr)
			}
		}
	}
	slices.SortStableFunc(merged, func(a, b PullRequest) int { return b.Updated.Compare(a.Updated) })
	// What is left to resume would all fall past the kept list.
	if next.Resume != "" && len(merged) >= MaxPulls && merged[MaxPulls-1].Updated.After(next.Oldest) {
		next = PullProgress{Cursor: next.Cursor}
	}
	merged = merged[:min(len(merged), MaxPulls)]
	kept := pullCache{Repo: repo, PullProgress: next, Fetched: started, Pulls: merged}
	if fetchErr != nil {
		kept.Fetched = cached.Fetched
	}
	data, err := json.Marshal(kept)
	var redacted pullCache
	if err == nil {
		data = []byte(redact(string(data)))
		err = json.Unmarshal(data, &redacted)
	}
	if err != nil {
		return nil, errors.New("invalid pull request data")
	}
	if path != "" {
		cache.WriteFile(path, data)
	}
	return redacted.Pulls, fetchErr
}

// redactPulls redacts a cached list again, for secrets registered since it
// was written.
func redactPulls(cached pullCache, redact func(string) string) ([]PullRequest, error) {
	data, err := json.Marshal(cached.Pulls)
	var pulls []PullRequest
	if err == nil {
		err = json.Unmarshal([]byte(redact(string(data))), &pulls)
	}
	if err != nil {
		return nil, errors.New("invalid pull request data")
	}
	return pulls, nil
}

const (
	// reviewAge is how long a review read for one head is reused, as often
	// as the pull request list refreshes.
	reviewAge = time.Minute
	// maxReviews bounds the reviews kept, the newest read.
	maxReviews = 200
)

// reviewEntry is a review read for the head its caller named.
type reviewEntry struct {
	Head    string
	Fetched time.Time
	Review  Review
}

type reviewCache struct {
	Repo    string
	Reviews map[int]reviewEntry
}

// Reviews keeps a forge's pull request reviews by number, in memory and in a
// .reviews.json beside its pull request list. Its lock keeps a review read
// from waiting on a list refresh.
type Reviews struct {
	mu         sync.Mutex
	path, repo string
	kept       map[int]reviewEntry
}

// NewReviews keeps repo's reviews beside the pull request list at pullsPath,
// or only in memory when it is empty.
func NewReviews(pullsPath, repo string) *Reviews {
	r := &Reviews{repo: repo}
	if pullsPath != "" {
		r.path = strings.TrimSuffix(pullsPath, ".pulls.json") + ".reviews.json"
	}
	return r
}

// Path is the reviews file, or empty when kept only in memory.
func (r *Reviews) Path() string { return r.path }

// Get reuses a review read for the same head in the last minute unless fresh,
// else reads one, which must already be redacted. A failed read returns the
// last one kept, with ok, and the error.
func (r *Reviews) Get(number int, head string, fresh bool, read func() (Review, error)) (Review, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.kept == nil {
		var kept reviewCache
		if data, err := os.ReadFile(r.path); err == nil && json.Unmarshal(data, &kept) == nil && kept.Repo == r.repo {
			r.kept = kept.Reviews
		}
		if r.kept == nil {
			r.kept = map[int]reviewEntry{}
		}
	}
	kept, ok := r.kept[number]
	kept.Review.Fetched = kept.Fetched
	if ok && !fresh && kept.Head == head && time.Since(kept.Fetched) < reviewAge {
		return kept.Review, true, nil
	}
	review, err := read()
	if err != nil {
		return kept.Review, ok, err
	}
	review.Fetched = time.Now()
	r.kept[number] = reviewEntry{Head: head, Fetched: review.Fetched, Review: review}
	if len(r.kept) > maxReviews {
		numbers := make([]int, 0, len(r.kept))
		for n := range r.kept {
			numbers = append(numbers, n)
		}
		slices.SortFunc(numbers, func(a, b int) int { return r.kept[b].Fetched.Compare(r.kept[a].Fetched) })
		for _, n := range numbers[maxReviews:] {
			delete(r.kept, n)
		}
	}
	if r.path != "" {
		if data, err := json.Marshal(reviewCache{Repo: r.repo, Reviews: r.kept}); err == nil {
			cache.WriteFile(r.path, data)
		}
	}
	return review, true, nil
}
