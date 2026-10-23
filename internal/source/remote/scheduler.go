package remote

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ScheduleDiagnostic reports backoff to the source worker instead of sleeping
// behind a quota or retrying a potentially applied write. Derived from Worklease
// v1.9.3 scheduler.go's per-account queues and retry deadlines.
type ScheduleDiagnostic struct {
	Code    string
	RetryAt time.Time
}

func (e ScheduleDiagnostic) Error() string { return e.Code }

type accountQueue struct {
	slot    chan struct{}
	pending chan struct{}
	retryAt time.Time // protected by slot, including response body reads
	etags   map[[32]byte]string
}
type Scheduler struct {
	mu     sync.Mutex
	queues map[string]*accountQueue
	now    func() time.Time
}

func NewScheduler() *Scheduler { return &Scheduler{now: time.Now, queues: map[string]*accountQueue{}} }

var sharedScheduler = NewScheduler()

func (s *Scheduler) queue(host, account string) *accountQueue {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := strings.ToLower(host) + "\x00" + account
	q := s.queues[key]
	if q == nil {
		q = &accountQueue{slot: make(chan struct{}, 1), pending: make(chan struct{}, 128), etags: map[[32]byte]string{}}
		s.queues[key] = q
	}
	return q
}

// Response owns a bounded body and redacted headers, not an http.Response with
// its credential-bearing Request. ETags are validators only: a 304 never means
// an empty list; adapters must reuse the corresponding representation.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}
type Client struct {
	Host, Account string
	HTTP          *http.Client
	Scheduler     *Scheduler // nil uses the process-wide host/account scheduler
	Secrets       *Secrets
}

// Do serializes requests through body consumption. It never retries a write,
// follows a redirect, returns raw network errors, or persists authorization.
func (c Client) Do(ctx context.Context, request *http.Request, credential Credential) (Response, error) {
	fail := func(code string) (Response, error) { return Response{}, fmt.Errorf("remote HTTP: %s", code) }
	if request == nil || request.URL == nil || request.URL.Scheme != "https" || request.URL.User != nil || !strings.EqualFold(request.URL.Host, c.Host) || c.Account == "" || c.Secrets == nil || credential.token == "" {
		return fail("HTTPS host, account and explicit credential required")
	}
	c.Secrets.add(credential.token)
	s := c.Scheduler
	if s == nil {
		s = sharedScheduler
	}
	q := s.queue(c.Host, c.Account)
	select {
	case q.pending <- struct{}{}:
	default:
		return Response{}, ScheduleDiagnostic{Code: "overloaded"}
	}
	defer func() { <-q.pending }()
	select {
	case q.slot <- struct{}{}:
	case <-ctx.Done():
		return fail("canceled")
	}
	defer func() { <-q.slot }()
	if err := ctx.Err(); err != nil {
		return fail("canceled")
	}
	if q.retryAt.After(s.now()) {
		return Response{}, ScheduleDiagnostic{Code: "rate-limited", RetryAt: q.retryAt}
	}
	req := request.Clone(ctx)
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	req.Header.Set("Authorization", "Bearer "+credential.token)
	key := sha256.Sum256([]byte(req.Method + "\x00" + req.URL.String() + "\x00" + req.Header.Get("Accept") + "\x00" + credential.token))
	cacheable := req.Method == http.MethodGet && req.Body == nil
	if cacheable && q.etags[key] != "" {
		req.Header.Set("If-None-Match", q.etags[key])
	}
	client := http.Client{Timeout: 15 * time.Second}
	if c.HTTP != nil {
		client = *c.HTTP
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(req)
	if err != nil {
		return fail("request failed")
	}
	defer response.Body.Close()
	now := s.now()
	until := retryDeadline(response.Header, now)
	limited := response.StatusCode == 429 || response.StatusCode == 403 && !until.IsZero()
	// Header-declared limits take effect before consuming a possibly blocked body.
	var raw []byte
	if !limited {
		raw, err = io.ReadAll(io.LimitReader(response.Body, (8<<20)+1))
		if err != nil || len(raw) > 8<<20 {
			return fail("response failed or exceeded 8MiB")
		}
		limited = response.StatusCode == 403 && strings.Contains(strings.ToLower(string(raw)), "rate limit")
	}
	if limited && !until.After(now) {
		until = now.Add(time.Second)
	}
	if until.After(q.retryAt) {
		q.retryAt = until
	}
	if limited {
		return Response{}, ScheduleDiagnostic{Code: "rate-limited", RetryAt: q.retryAt}
	}
	if response.StatusCode >= 300 && response.StatusCode != http.StatusNotModified {
		return fail("provider refused request")
	}
	if cacheable && response.StatusCode == http.StatusOK {
		if len(q.etags) >= 128 {
			clear(q.etags)
		}
		if etag := response.Header.Get("ETag"); etag != "" && c.Secrets.Redact(etag) == etag {
			q.etags[key] = etag
		} else {
			delete(q.etags, key)
		}
	}
	headers := response.Header.Clone()
	for key, values := range headers {
		for i, value := range values {
			headers[key][i] = c.Secrets.Redact(value)
		}
	}
	return Response{Status: response.StatusCode, Header: headers, Body: []byte(c.Secrets.Redact(string(raw)))}, nil
}
func retryDeadline(header http.Header, now time.Time) time.Time {
	var until time.Time
	if seconds, err := strconv.ParseInt(header.Get("Retry-After"), 10, 32); err == nil && seconds >= 0 {
		until = now.Add(time.Duration(seconds) * time.Second)
	} else if date, err := http.ParseTime(header.Get("Retry-After")); err == nil {
		until = date
	}
	for _, prefix := range []string{"X-RateLimit-", "RateLimit-"} {
		if header.Get(prefix+"Remaining") == "0" {
			if reset, err := strconv.ParseInt(header.Get(prefix+"Reset"), 10, 64); err == nil {
				deadline := time.Unix(reset, 0)
				if prefix == "RateLimit-" {
					deadline = now.Add(time.Duration(reset) * time.Second)
				}
				if deadline.After(until) {
					until = deadline
				}
			}
		}
	}
	return until
}
