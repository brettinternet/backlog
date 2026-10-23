package remote

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Catches requests escaping an account's quota window, including the final
// successful response exhausting quota, and ETags being treated as empty data.
func TestHTTPQuotaAndETag(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	scheduler := NewScheduler()
	var clock atomic.Int64
	clock.Store(now.Unix())
	scheduler.now = func() time.Time { return time.Unix(clock.Load(), 0) }
	secrets := &Secrets{}
	credential := Credential{token: "canary-private-token"}
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+credential.token {
			t.Error("missing explicit credential")
		}
		switch call {
		case 1:
			w.Header().Set("Retry-After", "10")
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", fmt.Sprint(scheduler.now().Add(30*time.Second).Unix()))
			w.WriteHeader(429)
		case 2:
			w.Header().Set("ETag", `"revision-1"`)
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", fmt.Sprint(scheduler.now().Add(20*time.Second).Unix()))
			fmt.Fprint(w, `{"title":"canary-private-token"}`)
		case 3:
			if r.Header.Get("If-None-Match") != `"revision-1"` {
				t.Error("lost ETag")
			}
			w.WriteHeader(304)
		case 4:
			w.Header().Set("Retry-After", scheduler.now().Add(time.Minute).UTC().Format(http.TimeFormat))
			w.WriteHeader(429)
		case 5:
			// Quota must apply before consuming a broken or blocked body.
			w.Header().Set("Retry-After", "10")
			w.WriteHeader(429)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		default:
			t.Error("unexpected request")
		}
	}))
	defer server.Close()
	parsed, _ := url.Parse(server.URL)
	client := Client{Host: parsed.Host, Account: "alice", HTTP: server.Client(), Scheduler: scheduler, Secrets: secrets}
	request, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	do := func() (Response, error) { return client.Do(ctx, request, credential) }
	_, err := do()
	var limited ScheduleDiagnostic
	if !errors.As(err, &limited) || !limited.RetryAt.Equal(now.Add(30*time.Second)) {
		t.Fatalf("quota: %v", err)
	}
	if _, err = do(); !errors.As(err, &limited) || calls.Load() != 1 {
		t.Fatalf("ignored backoff: %v calls=%d", err, calls.Load())
	}
	now = now.Add(30 * time.Second)
	clock.Store(now.Unix())
	response, err := do()
	if err != nil || strings.Contains(string(response.Body), credential.token) {
		t.Fatalf("unsafe response: %s %v", response.Body, err)
	}
	if _, err = do(); !errors.As(err, &limited) || calls.Load() != 2 {
		t.Fatalf("ignored successful quota exhaustion: %v calls=%d", err, calls.Load())
	}
	now = now.Add(20 * time.Second)
	clock.Store(now.Unix())
	response, err = do()
	if err != nil || response.Status != 304 {
		t.Fatalf("conditional GET: %+v %v", response, err)
	}
	_, err = do()
	if !errors.As(err, &limited) || !limited.RetryAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("HTTP-date Retry-After: %+v deadline=%v want=%v calls=%d", err, limited.RetryAt, now.Add(time.Minute), calls.Load())
	}
	now = now.Add(time.Minute)
	clock.Store(now.Unix())
	_, err = do()
	if !errors.As(err, &limited) || !limited.RetryAt.Equal(now.Add(10*time.Second)) {
		t.Fatalf("quota waited on response body: %v", err)
	}
}

// Catches independent clients bypassing serialization or a blocked account
// holding the scheduler globally. Channels, not elapsed time, establish order.
func TestHTTPAccountSerializationAndIsolation(t *testing.T) {
	t.Parallel()
	scheduler := NewScheduler()
	entered, release := make(chan string, 3), make(chan struct{})
	var active, maxActive atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("Authorization")
		if token == "Bearer alice" {
			n := active.Add(1)
			for old := maxActive.Load(); n > old && !maxActive.CompareAndSwap(old, n); old = maxActive.Load() {
			}
			defer active.Add(-1)
		}
		entered <- token
		select {
		case <-release:
		case <-r.Context().Done():
		}
		fmt.Fprint(w, "ok")
	}))
	defer server.Close()
	parsed, _ := url.Parse(server.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	results := make(chan error, 3)
	send := func(account string) {
		client := Client{Host: parsed.Host, Account: account, HTTP: server.Client(), Scheduler: scheduler, Secrets: &Secrets{}}
		req, _ := http.NewRequest(http.MethodGet, server.URL, nil)
		_, err := client.Do(ctx, req, Credential{token: account})
		results <- err
	}
	go send("alice")
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("first request did not start")
	}
	go send("alice")
	go send("bob")
	select {
	case token := <-entered:
		if token != "Bearer bob" {
			t.Fatal("same-account request overlapped")
		}
	case <-ctx.Done():
		t.Fatal("second account blocked behind alice")
	}
	close(release)
	for range 3 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if maxActive.Load() != 1 {
		t.Fatalf("account concurrency=%d", maxActive.Load())
	}
}
