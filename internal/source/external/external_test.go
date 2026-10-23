package external

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/source"
)

func TestExternalAdapterHelper(t *testing.T) {
	separator := -1
	for index, arg := range os.Args {
		if arg == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || separator+1 >= len(os.Args) {
		return
	}
	mode := os.Args[separator+1]
	marker := ""
	if separator+2 < len(os.Args) {
		marker = os.Args[separator+2]
	}
	runAdapterHelper(mode, marker)
}

func TestExternalSourceNegotiatesPagesAndAppliesOptimistically(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "starts")
	adapter := newTestSource(t, "normal", marker, time.Second)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("New spawned the adapter: stat error=%v", err)
	}
	if info := adapter.Info(); len(info.Capabilities) != 0 {
		t.Fatalf("Info started negotiation: %+v", info)
	}
	if err := adapter.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !adapter.Info().Capabilities.Allows(model.Set) {
		t.Fatal("negotiated set capability missing from Info")
	}
	if _, err := adapter.Apply(context.Background(), model.Mutation{Ref: model.Ref{Source: "external-test", ID: "REF-1"}, Op: model.Set}); err == nil {
		t.Fatal("mutation without expected version was accepted")
	}
	if _, err := adapter.Apply(context.Background(), model.Mutation{Ref: model.Ref{Source: "external-test", ID: "REF-1"}, ExpectedVersion: "v1", Op: model.RemoveCriterion}); err == nil {
		t.Fatal("unsupported mutation was accepted")
	}
	delta, err := adapter.Load(context.Background(), source.Cursor{})
	if err != nil || delta.Coverage != source.Complete || len(delta.Items) != 2 {
		t.Fatalf("Load = %+v, %v", delta, err)
	}
	if delta.Items[0].Ref != (model.Ref{Source: "external-test", ID: "REF-1"}) || delta.Items[1].Ref.ID != "REF-2" {
		t.Fatalf("unexpected paged item refs: %+v", delta.Items)
	}
	read, err := adapter.Read(context.Background(), "REF-1")
	if err != nil || read.Version != "v1" {
		t.Fatalf("Read = %+v, %v", read, err)
	}
	updated, err := adapter.Apply(context.Background(), model.Mutation{
		Ref: model.Ref{Source: "external-test", ID: "REF-1"}, ExpectedVersion: "v1", Op: model.Set, Field: "title", Value: "Updated",
	})
	if err != nil || updated.Ref.ID != "REF-1" || updated.Version != "v2" {
		t.Fatalf("Apply = %+v, %v", updated, err)
	}
	if err := adapter.Close(); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(readMarker(t, marker)); got != "start" {
		t.Fatalf("adapter starts=%q, want one start", got)
	}
}

func TestExternalApplyReportsUnknownOutcomeWithoutReplay(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "starts")
	adapter := newTestSource(t, "crash-apply", marker, time.Second)
	_, err := adapter.Apply(context.Background(), model.Mutation{
		Ref: model.Ref{Source: "external-test", ID: "REF-1"}, ExpectedVersion: "v1", Op: model.Set, Field: "title", Value: "Updated",
	})
	var refusal *source.Error
	if !errors.As(err, &refusal) || refusal.Reason != "unknown-outcome" {
		t.Fatalf("lost apply response error = %v, want unknown-outcome", err)
	}
	if got := strings.TrimSpace(readMarker(t, marker)); got != "start\napply" {
		t.Fatalf("lost write response replayed: trace=%q", got)
	}
}

func TestExternalProcessFailuresAreBoundedAndReaped(t *testing.T) {
	tests := []struct {
		name string
		mode string
		want string
	}{
		{name: "malformed frame", mode: "malformed-frame"},
		// Missing diagnostics must not be accepted as valid provider failures: the
		// frozen schema requires structured safe error data in every error envelope.
		{name: "missing error data", mode: "missing-error-data", want: "diagnostic"},
		{name: "missing error diagnostic", mode: "missing-error-diagnostic", want: "diagnostic"},
		{name: "oversized result", mode: "oversized-result"},
		{name: "oversized frame", mode: "oversized-frame", want: "frame"},
		{name: "empty manifest version", mode: "bad-version", want: "version"},
		{name: "capability disagreement", mode: "bad-capabilities", want: "capabilit"},
		{name: "unknown required feature", mode: "bad-feature"},
		{name: "repeated page cursor", mode: "repeat-cursor"},
		{name: "crashed adapter", mode: "crash-list"},
		{name: "hung adapter", mode: "hang-list", want: "deadline"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter := newTestSource(t, test.mode, "", 150*time.Millisecond)
			_, err := adapter.Load(context.Background(), source.Cursor{})
			if err == nil {
				t.Fatal("Load unexpectedly succeeded")
			}
			if test.want != "" && !strings.Contains(strings.ToLower(err.Error()), test.want) {
				t.Fatalf("Load error %q does not include %q", err, test.want)
			}
			adapter.mu.Lock()
			run := adapter.run
			adapter.mu.Unlock()
			if run != nil {
				select {
				case <-run.reaped:
				case <-time.After(time.Second):
					t.Fatal("failed adapter process was not reaped")
				}
			}
		})
	}
}

func TestExternalProcessBackoffAndCallerCancellation(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "starts")
	adapter := newTestSource(t, "crash-list", marker, time.Second)
	if _, err := adapter.Load(context.Background(), source.Cursor{}); err == nil {
		t.Fatal("crashed adapter succeeded")
	}
	if got := strings.TrimSpace(readMarker(t, marker)); got != "start" {
		t.Fatalf("adapter starts after crash=%q", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	_, err := adapter.Load(ctx, source.Cursor{})
	cancel()
	if err == nil {
		t.Fatal("request during restart backoff unexpectedly succeeded")
	}
	if got := strings.TrimSpace(readMarker(t, marker)); got != "start" {
		t.Fatalf("backoff spawned adapter early: starts=%q", got)
	}
	adapter.now = func() time.Time { return time.Now().Add(time.Minute) }
	if _, err := adapter.Load(context.Background(), source.Cursor{}); err == nil {
		t.Fatal("second crashed adapter succeeded")
	}
	if got := strings.TrimSpace(readMarker(t, marker)); got != "start\nstart" {
		t.Fatalf("adapter restart count=%q", got)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	_, err = adapter.Load(ctx, source.Cursor{})
	cancel()
	if err == nil {
		t.Fatal("request during increased restart backoff unexpectedly succeeded")
	}
	if got := strings.TrimSpace(readMarker(t, marker)); got != "start\nstart" {
		t.Fatalf("increased backoff spawned adapter early: starts=%q", got)
	}

	cancelCtx, stop := context.WithCancel(context.Background())
	live := newTestSource(t, "normal", "", time.Second)
	if _, err := live.Load(cancelCtx, source.Cursor{}); err != nil {
		t.Fatal(err)
	}
	live.mu.Lock()
	run := live.run
	live.mu.Unlock()
	if run == nil {
		t.Fatal("successful Load left no live adapter process")
	}
	stop()
	select {
	case <-run.reaped:
	case <-time.After(time.Second):
		t.Fatal("caller cancellation did not reap the adapter")
	}
	if err := live.Close(); err != nil {
		t.Fatal(err)
	}
	idle := newTestSource(t, "normal", "", time.Second)
	if _, err := idle.Load(context.Background(), source.Cursor{}); err != nil {
		t.Fatal(err)
	}
	idle.mu.Lock()
	idleRun, idleID := idle.run, idle.run.idleID
	idle.mu.Unlock()
	idle.expireIdle(idleRun, idleID)
	select {
	case <-idleRun.reaped:
	case <-time.After(time.Second):
		t.Fatal("idle adapter was not reaped")
	}
	if err := idle.Close(); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestExternalCheckRunsInitializeCapabilitiesListAndReferenceRead(t *testing.T) {
	if err := Check(context.Background(), testOptions(t, "normal", "", time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := Check(context.Background(), testOptions(t, "noncanonical-id", "", time.Second)); err != nil {
		t.Fatalf("Check assumed a fixed provider item ID: %v", err)
	}
}

func FuzzValidateFrame(f *testing.F) {
	for _, frame := range [][]byte{
		[]byte("{\"jsonrpc\":\"2.0\"}\n"),
		[]byte("{\"a\":1,\"a\":2}\n"),
		[]byte("{ \"a\":1}\n"),
		[]byte("{\"a\":1}"),
		[]byte("\xef\xbb\xbf{}\n"),
	} {
		f.Add(frame)
	}
	f.Fuzz(func(t *testing.T, frame []byte) {
		_ = ValidateFrame(frame)
	})
}

func newTestSource(t *testing.T, mode, marker string, timeout time.Duration) *Source {
	t.Helper()
	adapter, err := New(testOptions(t, mode, marker, timeout))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Close() })
	return adapter
}

func testOptions(t *testing.T, mode, marker string, timeout time.Duration) Options {
	t.Helper()
	binary, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	return Options{
		ID:      "external-test",
		Run:     []string{binary, "-test.run=^TestExternalAdapterHelper$", "--", mode, marker},
		Timeout: timeout,
	}
}

func readMarker(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

func runAdapterHelper(mode, marker string) {
	if marker != "" {
		file, err := os.OpenFile(marker, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			os.Exit(40)
		}
		_, _ = fmt.Fprintln(file, "start")
		_ = file.Close()
	}
	reader := bufio.NewReader(os.Stdin)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			os.Exit(0)
		}
		var request struct {
			ID     string          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(line, &request) != nil || request.ID == "" {
			os.Exit(41)
		}
		var params map[string]json.RawMessage
		if json.Unmarshal(request.Params, &params) != nil {
			os.Exit(42)
		}
		switch request.Method {
		case "initialize":
			version := "1.0"
			if mode == "bad-version" {
				version = ""
			}
			capabilities := []string{"list", "read", "set"}
			features := []string{}
			if mode == "bad-feature" {
				features = []string{"unknown-feature"}
			}
			respond(request.ID, map[string]any{"protocolVersion": 1, "manifest": map[string]any{
				"id": "test-adapter", "version": version, "capabilities": capabilities, "requiredFeatures": features,
			}})
		case "capabilities":
			caps := []string{"list", "read", "set"}
			if mode == "bad-capabilities" {
				caps = []string{"list", "read"}
			}
			respond(request.ID, map[string]any{"capabilities": caps})
		case "list":
			switch mode {
			case "missing-error-data", "missing-error-diagnostic":
				rpcError := map[string]any{"code": -32603, "message": "failure"}
				if mode == "missing-error-diagnostic" {
					rpcError["data"] = map[string]any{}
				}
				_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": rpcError})
				continue
			case "crash-list":
				os.Exit(43)
			case "hang-list":
				ticker := time.NewTicker(time.Hour)
				for range ticker.C {
				}
			case "malformed-frame":
				_, _ = io.WriteString(os.Stdout, "{\"jsonrpc\":\"2.0\",\"id\":\""+request.ID+"\",\"id\":\"duplicate\",\"result\":{}}\n")
				continue
			case "oversized-result":
				respond(request.ID, map[string]any{"items": []model.Item{}, "nextCursor": "", "coverage": "complete", "padding": strings.Repeat("x", resultLimit+1)})
				continue
			case "oversized-frame":
				respond(request.ID, map[string]any{"items": []model.Item{}, "nextCursor": "", "coverage": "complete", "padding": strings.Repeat("x", frameLimit+1)})
				continue
			}
			var cursor string
			_ = json.Unmarshal(params["cursor"], &cursor)
			firstID, secondID := "REF-1", "REF-2"
			if mode == "noncanonical-id" {
				firstID, secondID = "TASK-7", "TASK-8"
			}
			if cursor == "" {
				respond(request.ID, map[string]any{"items": []model.Item{helperItem("external-test", firstID, "v1")}, "nextCursor": "page-2", "coverage": "complete"})
			} else if mode == "repeat-cursor" {
				respond(request.ID, map[string]any{"items": []model.Item{helperItem("external-test", secondID, "v1")}, "nextCursor": "page-2", "coverage": "complete"})
			} else {
				respond(request.ID, map[string]any{"items": []model.Item{helperItem("external-test", secondID, "v1")}, "nextCursor": "", "coverage": "complete"})
			}
		case "read":
			var id string
			_ = json.Unmarshal(params["id"], &id)
			if id == "REF-1" || id == "REF-2" || id == "TASK-7" || id == "TASK-8" {
				respond(request.ID, map[string]any{"item": helperItem("external-test", id, "v1")})
			} else {
				respondError(request.ID, -32007)
			}
		case "apply":
			if mode == "crash-apply" {
				file, err := os.OpenFile(marker, os.O_APPEND|os.O_WRONLY, 0o600)
				if err != nil {
					os.Exit(49)
				}
				_, _ = fmt.Fprintln(file, "apply")
				_ = file.Close()
				os.Exit(48)
			}
			var mutation map[string]json.RawMessage
			_ = json.Unmarshal(params["mutation"], &mutation)
			if mutation["Ref"] == nil || mutation["ExpectedVersion"] == nil || mutation["Op"] == nil {
				os.Exit(44)
			}
			var ref model.Ref
			_ = json.Unmarshal(mutation["Ref"], &ref)
			respond(request.ID, map[string]any{"item": helperItem(ref.Source, ref.ID, "v2")})
		default:
			os.Exit(45)
		}
	}
}

func helperItem(sourceID, id, version string) model.Item {
	return model.Item{Ref: model.Ref{Source: sourceID, ID: id}, Title: id, Version: version}
}

func respond(id string, result any) {
	frame, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	if err != nil {
		os.Exit(46)
	}
	frame = append(frame, '\n')
	for len(frame) != 0 {
		n, err := os.Stdout.Write(frame)
		frame = frame[n:]
		if err != nil || n == 0 {
			os.Exit(47)
		}
	}
}

func respondError(id string, code int) {
	frame, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": "not found", "data": map[string]any{"diagnostic": "not-found"}}})
	_, _ = bytes.NewBuffer(frame).WriteTo(os.Stdout)
	_, _ = io.WriteString(os.Stdout, "\n")
}
