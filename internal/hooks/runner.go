package hooks

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/query"
	"github.com/brettinternet/backlog/internal/source"
)

const (
	resultLimit      = 8 << 10
	afterConcurrency = 4
	defaultTimeout   = 30 * time.Second
)

// HasBefore reports whether a trusted before hook subscribes to this event type.
func HasBefore(cfg project.Config, eventType string) bool {
	for i, hook := range cfg.Hooks {
		if phase(hook) == "before" && hook.On == eventType && cfg.RequireTrusted(fmt.Sprintf("hooks[%d]", i)) == nil {
			return true
		}
	}
	return false
}

// Before runs matching before hooks in declaration order. The first failure
// rejects the mutation, using its first stderr line as the stable message.
func Before(ctx context.Context, cfg project.Config, event Event, cwd string) error {
	if event.Depth < 0 || event.Depth > 3 {
		return nil
	}
	for i, hook := range cfg.Hooks {
		if phase(hook) != "before" || hook.On != event.Type || cfg.RequireTrusted(fmt.Sprintf("hooks[%d]", i)) != nil {
			continue
		}
		matched, err := matches(cfg, hook, event)
		if err != nil {
			return source.Refuse("rejected", err.Error())
		}
		if !matched {
			continue
		}
		result := execute(ctx, cfg, hook, event, cwd, false)
		if result.outcome != "ok" {
			message := firstLine(result.stderr)
			if message == "" {
				if result.outcome == "timeout" {
					message = "before hook " + hook.Name + " timed out"
				} else if result.message != "" {
					message = result.message
				} else {
					message = "before hook " + hook.Name + " failed"
				}
			}
			return source.Refuse("rejected", message)
		}
	}
	return nil
}

// StartRunner detaches one bounded runner for events this process appended.
// Event-log append ownership is the only dispatch point; duplicate appends do
// not appear in events and therefore cannot start another runner.
func StartRunner(cfg project.Config, events []Event, cwd string) error {
	if len(events) == 0 || !hasTrustedAfterHooks(cfg, events) {
		return nil
	}
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return err
		}
	}
	ids := make([]string, 0, len(events))
	for _, event := range events {
		if event.Depth <= 3 && hasTrustedAfterHook(cfg, event.Type) {
			ids = append(ids, event.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	args := []string{"-C", cwd, "hooks", "run"}
	args = append(args, ids...)
	cmd := exec.Command(executable, args...)
	cmd.Dir = cwd
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// RunEvents executes after hooks for logged events. Each event owns its
// finished events and recursively dispatches its chains to a maximum depth of 3.
func RunEvents(ctx context.Context, cfg project.Config, ids []string, cwd string) error {
	log := New(cfg.Paths.StateDir, cfg.Project.ID)
	for _, id := range ids {
		event, err := log.Event(ctx, id)
		if err != nil {
			return err
		}
		if err = runEvent(ctx, cfg, log, event, cwd); err != nil {
			return err
		}
	}
	return nil
}

// RunEvent executes and dispatches one event. It is also the synchronous seam
// used by tests and non-CLI event producers.
func RunEvent(ctx context.Context, cfg project.Config, event Event, cwd string) error {
	return runEvent(ctx, cfg, New(cfg.Paths.StateDir, cfg.Project.ID), event, cwd)
}

func runEvent(ctx context.Context, cfg project.Config, log Log, event Event, cwd string) error {
	if event.Depth < 0 || event.Depth > 3 {
		return nil
	}
	type selected struct {
		hook     project.HookConfig
		matchErr error
	}
	var selectedHooks []selected
	for i, hook := range cfg.Hooks {
		if phase(hook) != "after" || hook.On != event.Type || cfg.RequireTrusted(fmt.Sprintf("hooks[%d]", i)) != nil {
			continue
		}
		matched, err := matches(cfg, hook, event)
		if err != nil {
			selectedHooks = append(selectedHooks, selected{hook: hook, matchErr: err})
			continue
		}
		if matched {
			selectedHooks = append(selectedHooks, selected{hook: hook})
		}
	}
	if len(selectedHooks) == 0 {
		return nil
	}
	results := make([]execution, len(selectedHooks))
	sem := make(chan struct{}, afterConcurrency)
	var workers sync.WaitGroup
	for n, candidate := range selectedHooks {
		workers.Add(1)
		go func(n int, candidate selected) {
			defer workers.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results[n] = execution{outcome: "failed", exitCode: -1, message: ctx.Err().Error()}
				return
			}
			if candidate.matchErr != nil {
				results[n] = execution{outcome: "refused", exitCode: -1, message: candidate.matchErr.Error()}
				return
			}
			results[n] = execute(ctx, cfg, candidate.hook, event, cwd, true)
		}(n, candidate)
	}
	workers.Wait()

	for n, candidate := range selectedHooks {
		result := results[n]
		finished := Event{
			Type: "hook.finished", Project: event.Project, Source: event.Source,
			Ref: event.Ref, Resource: event.Resource, Item: event.Item, Actor: event.Actor,
			Origin: "hook", Cause: stringPointer(event.ID), Depth: event.Depth + 1,
			Name: candidate.hook.Name, Outcome: result.outcome,
			ExitCode: intPointer(result.exitCode), Duration: result.duration,
			Result:   result.result,
			Versions: []string{event.ID, candidate.hook.Name, hookVersion(candidate.hook)},
		}
		var appended bool
		var logged Event
		if err := log.Update(ctx, func(tx *Transaction) error {
			var err error
			appended, err = tx.Append(finished)
			if appended && err == nil {
				logged = tx.Appended()[0]
			}
			return err
		}); err != nil {
			return err
		}
		if appended && logged.Depth <= 3 {
			if err := runEvent(ctx, cfg, log, logged, cwd); err != nil {
				return err
			}
		}
	}
	return nil
}

type execution struct {
	outcome  string
	exitCode int
	duration time.Duration
	result   map[string]any
	stdout   []byte
	stderr   []byte
	message  string
}

func execute(ctx context.Context, cfg project.Config, hook project.HookConfig, event Event, cwd string, captureResult bool) execution {
	return executeWithOutput(ctx, cfg, hook, event, cwd, captureResult, nil, nil)
}

func executeWithOutput(ctx context.Context, cfg project.Config, hook project.HookConfig, event Event, cwd string, captureResult bool, out, errOut io.Writer) execution {
	argv := make([]string, len(hook.Run))
	for i, arg := range hook.Run {
		expanded, err := expand(arg, cfg, event)
		if err != nil {
			return execution{outcome: "refused", exitCode: -1, message: err.Error()}
		}
		argv[i] = expanded
	}
	if len(argv) == 0 || argv[0] == "" {
		return execution{outcome: "refused", exitCode: -1, message: "hook run is empty"}
	}
	runCWD := cwd
	if runCWD == "" {
		var err error
		runCWD, err = os.Getwd()
		if err != nil {
			return execution{outcome: "failed", exitCode: -1, message: err.Error()}
		}
	}
	if hook.CWD != "" {
		expanded, err := expand(hook.CWD, cfg, event)
		if err != nil {
			return execution{outcome: "refused", exitCode: -1, message: err.Error()}
		}
		runCWD = expanded
		if !filepath.IsAbs(runCWD) {
			runCWD = filepath.Join(cwd, runCWD)
		}
	}
	resultFile, err := os.CreateTemp("", "bl-hook-result-*")
	if err != nil {
		return execution{outcome: "failed", exitCode: -1, message: err.Error()}
	}
	resultPath := resultFile.Name()
	if err = resultFile.Close(); err != nil {
		_ = os.Remove(resultPath)
		return execution{outcome: "failed", exitCode: -1, message: err.Error()}
	}
	defer os.Remove(resultPath)

	timeout := defaultTimeout
	if hook.Timeout != "" {
		timeout, err = time.ParseDuration(hook.Timeout)
		if err != nil || timeout <= 0 {
			return execution{outcome: "refused", exitCode: -1, message: "invalid hook timeout"}
		}
	}
	hookCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	data, err := json.Marshal(event)
	if err != nil {
		return execution{outcome: "failed", exitCode: -1, message: err.Error()}
	}
	cmd := exec.CommandContext(hookCtx, argv[0], argv[1:]...)
	cmd.Dir = runCWD
	cmd.Stdin = bytes.NewReader(data)
	var stdout, stderr limitedBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if out != nil {
		cmd.Stdout = io.MultiWriter(&stdout, out)
	}
	if errOut != nil {
		cmd.Stderr = io.MultiWriter(&stderr, errOut)
	}
	cmd.Env = hookEnvironment(cfg, event, resultPath)
	started := time.Now()
	err = cmd.Run()
	elapsed := time.Since(started)
	result := execution{outcome: "ok", exitCode: 0, duration: elapsed, stdout: stdout.Bytes(), stderr: stderr.Bytes()}
	if err != nil {
		result.outcome = "failed"
		result.exitCode = -1
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			result.exitCode = exit.ExitCode()
		}
		if errors.Is(hookCtx.Err(), context.DeadlineExceeded) {
			result.outcome = "timeout"
			result.exitCode = 124
		} else if hookCtx.Err() != nil {
			result.message = hookCtx.Err().Error()
		} else {
			result.message = err.Error()
		}
	}
	if captureResult {
		parsed, present, resultErr := hookResult(resultPath, result.stdout, stdout.overflow)
		if resultErr != nil {
			if result.outcome == "ok" {
				result.outcome = "failed"
			}
			if result.message == "" {
				result.message = resultErr.Error()
			}
		} else if present {
			result.result = parsed
		}
	}
	return result
}

func hookEnvironment(cfg project.Config, event Event, resultPath string) []string {
	values := map[string]string{"BL_ITEM_ID": "", "BL_ITEM_REF": event.Ref, "BL_SOURCE": event.Source,
		"BL_PROJECT_ID": event.Project, "BL_ROOT": cfg.Discovery.ControlRoot,
		"BL_BRANCH": "", "BL_EVENT_ID": event.ID, "BL_HOOK_DEPTH": strconv.Itoa(event.Depth),
		"BL_RESULT": resultPath}
	if event.Name != "" && event.Type == "" {
		values["BL_EVENT_ORIGIN"] = "action"
	}
	item := eventItem(event)
	if item.Ref.ID != "" {
		values["BL_ITEM_ID"] = item.Ref.ID
	}
	values["BL_BRANCH"] = model.BranchName(item)
	if event.Resource != "" {
		values["BL_RESOURCE"] = event.Resource
	}
	keys := []string{"PATH", "HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"}
	env := make([]string, 0, len(values)+len(keys))
	for _, key := range keys {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	for key, value := range values {
		env = append(env, key+"="+value)
	}
	sort.Strings(env)
	return env
}

func expand(input string, cfg project.Config, event Event) (string, error) {
	item := eventItem(event)
	values := map[string]string{"id": item.Ref.ID, "ref": event.Ref, "source": event.Source,
		"url": item.URL, "root": cfg.Discovery.ControlRoot, "branch": model.BranchName(item),
		"resource": event.Resource}
	var out strings.Builder
	for n := 0; n < len(input); {
		if input[n] != '{' {
			out.WriteByte(input[n])
			n++
			continue
		}
		end := strings.IndexByte(input[n+1:], '}')
		if end < 0 {
			return "", fmt.Errorf("unclosed hook placeholder")
		}
		end += n + 1
		key := input[n+1 : end]
		var value string
		var ok bool
		if strings.HasPrefix(key, "result.") {
			name := strings.TrimPrefix(key, "result.")
			if name == "" {
				return "", fmt.Errorf("invalid result placeholder {%s}", key)
			}
			value, ok = event.Result[name].(string)
		} else {
			value, ok = values[key]
		}
		if !ok {
			return "", fmt.Errorf("missing hook placeholder {%s}", key)
		}
		if err := validateValue(value); err != nil {
			return "", fmt.Errorf("refused hook placeholder {%s}: %w", key, err)
		}
		out.WriteString(value)
		n = end + 1
	}
	return out.String(), nil
}

func validateValue(value string) error {
	if value == "" {
		return fmt.Errorf("value is missing")
	}
	if strings.HasPrefix(value, "-") {
		return fmt.Errorf("value begins with -")
	}
	if len(value) > 1024 {
		return fmt.Errorf("value exceeds 1 KiB")
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return fmt.Errorf("value contains a control character")
		}
	}
	return nil
}

func hookResult(path string, stdout []byte, overflow bool) (map[string]any, bool, error) {
	info, err := os.Lstat(path)
	var fileData []byte
	if err == nil {
		if !info.Mode().IsRegular() {
			return nil, false, fmt.Errorf("BL_RESULT is not a regular file")
		}
		if info.Size() > resultLimit {
			return nil, false, fmt.Errorf("BL_RESULT exceeds 8 KiB")
		}
		file, openErr := os.Open(path)
		if openErr != nil {
			return nil, false, openErr
		}
		fileData, err = io.ReadAll(io.LimitReader(file, resultLimit+1))
		closeErr := file.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			return nil, false, err
		}
		if len(fileData) > resultLimit {
			return nil, false, fmt.Errorf("BL_RESULT exceeds 8 KiB")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	if len(bytes.TrimSpace(fileData)) > 0 {
		result, err := decodeResult(fileData)
		return result, true, err
	}
	trimmed := bytes.TrimSpace(stdout)
	if len(trimmed) == 0 {
		return nil, false, nil
	}
	if trimmed[0] != '{' {
		return nil, false, nil
	}
	if overflow || len(stdout) > resultLimit {
		return nil, false, fmt.Errorf("stdout result exceeds 8 KiB")
	}
	result, err := decodeResult(trimmed)
	return result, true, err
}

func decodeResult(data []byte) (map[string]any, error) {
	if len(data) > resultLimit {
		return nil, fmt.Errorf("result exceeds 8 KiB")
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, fmt.Errorf("result must be one JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	var result map[string]any
	if err := decoder.Decode(&result); err != nil || result == nil {
		if err == nil {
			err = fmt.Errorf("result must be a JSON object")
		}
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("result contains more than one JSON value")
		}
		return nil, err
	}
	return result, nil
}

func matches(cfg project.Config, hook project.HookConfig, event Event) (bool, error) {
	if hook.If == "" {
		return true, nil
	}
	q, err := query.Parse(hook.If, query.Options{Hook: true})
	if err != nil {
		return false, err
	}
	item := eventItem(event)
	snapshot := query.Snapshot{Settings: cfg.Settings, Me: []string{cfg.Me.Default}, States: map[model.Ref]query.State{}}
	state := query.State{}
	if change, ok := event.Changes["readiness"]; ok {
		state.Readiness = fmt.Sprint(change.To)
	}
	if change, ok := event.Changes["archived"]; ok {
		state.Archived, _ = change.To.(bool)
	}
	snapshot.States[item.Ref] = state
	queryEvent := query.Event{Type: event.Type, Hook: event.Name, Outcome: event.Outcome}
	if event.Type == "action.finished" {
		queryEvent.Action = event.Name
	}
	for field := range event.Changes {
		queryEvent.Changed = append(queryEvent.Changed, field)
	}
	sort.Strings(queryEvent.Changed)
	return q.Match(item, snapshot, &queryEvent), nil
}

func eventItem(event Event) model.Item {
	var item model.Item
	if event.Item != nil {
		data, err := json.Marshal(event.Item)
		if err == nil {
			_ = json.Unmarshal(data, &item)
		}
	}
	if item.Ref.Source == "" {
		item.Ref.Source = event.Source
	}
	if item.Ref.ID == "" {
		_, item.Ref.ID, _ = strings.Cut(event.Ref, ":")
	}
	return item
}

func phase(hook project.HookConfig) string {
	if hook.Phase == "" {
		return "after"
	}
	return hook.Phase
}

func hasTrustedAfterHooks(cfg project.Config, events []Event) bool {
	for _, event := range events {
		if event.Depth <= 3 && hasTrustedAfterHook(cfg, event.Type) {
			return true
		}
	}
	return false
}

func hasTrustedAfterHook(cfg project.Config, eventType string) bool {
	for i, hook := range cfg.Hooks {
		if phase(hook) == "after" && hook.On == eventType && cfg.RequireTrusted(fmt.Sprintf("hooks[%d]", i)) == nil {
			return true
		}
	}
	return false
}

func hookVersion(hook project.HookConfig) string {
	data, _ := json.Marshal(hook)
	digest := sha256.Sum256(data)
	return fmt.Sprintf("%x", digest[:])
}

func firstLine(stderr []byte) string {
	line, _, _ := strings.Cut(string(stderr), "\n")
	return strings.TrimSpace(strings.TrimSuffix(line, "\r"))
}

func stringPointer(value string) *string { return &value }
func intPointer(value int) *int          { return &value }

type limitedBuffer struct {
	bytes.Buffer
	overflow bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	original := len(p)
	remaining := resultLimit - b.Len()
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.Buffer.Write(p)
	}
	if len(p) < original {
		b.overflow = true
	}
	return original, nil
}
