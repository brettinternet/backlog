// Package external hosts long-lived JSON-RPC adapters over newline-delimited stdio.
package external

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/source"
)

const (
	protocolMajor  = 1
	frameLimit     = 1 << 20
	resultLimit    = 786432
	maxPageItems   = 100
	defaultTimeout = 5 * time.Second
	idleTimeout    = 30 * time.Second
	watchInterval  = 5 * time.Second
)

type Options struct {
	ID      string
	Run     []string
	Dir     string
	Timeout time.Duration
}

type Source struct {
	options Options
	gate    chan struct{}

	mu           sync.Mutex
	run          *processRun
	closed       bool
	closeDone    chan struct{}
	closedCtx    context.Context
	closeCancel  context.CancelFunc
	stopping     map[*processRun]struct{}
	capabilities model.Capabilities
	manifest     manifest
	nextID       uint64
	restartAt    time.Time
	restartCount int
	now          func() time.Time
}

// New validates and copies adapter configuration without starting the process.
func New(options Options) (*Source, error) {
	if !validSourceID(options.ID) || len(options.Run) == 0 || strings.TrimSpace(options.Run[0]) == "" {
		return nil, source.Refuse("invalid-source", "external source requires an ID and executable argv")
	}
	if options.Dir != "" && !filepath.IsAbs(options.Dir) {
		return nil, source.Refuse("invalid-source", "external source directory must be absolute")
	}
	for _, arg := range options.Run {
		if !utf8.ValidString(arg) || strings.ContainsRune(arg, '\x00') {
			return nil, source.Refuse("invalid-source", "external source argv is invalid")
		}
	}
	if options.Timeout < 0 {
		return nil, source.Refuse("invalid-source", "external source timeout cannot be negative")
	}
	if options.Timeout == 0 {
		options.Timeout = defaultTimeout
	}
	options.Run = append([]string(nil), options.Run...)
	closedCtx, closeCancel := context.WithCancel(context.Background())
	return &Source{
		options:      options,
		gate:         make(chan struct{}, 1),
		closeDone:    make(chan struct{}),
		closedCtx:    closedCtx,
		closeCancel:  closeCancel,
		capabilities: model.Capabilities{},
		now:          time.Now,
	}, nil
}

var _ source.Source = (*Source)(nil)
var _ source.Preparer = (*Source)(nil)
var _ io.Closer = (*Source)(nil)

func (s *Source) Info() source.Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	capabilities := make(model.Capabilities, len(s.capabilities))
	for op, allowed := range s.capabilities {
		capabilities[op] = allowed
	}
	return source.Info{ID: s.options.ID, Kind: "external", Display: s.options.ID, Capabilities: capabilities}
}

// Initialize negotiates the adapter manifest and capabilities without listing items.
func (s *Source) Initialize(ctx context.Context) error {
	op, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer s.end(op)
	_, err = s.ensureInitialized(op)
	return err
}

func (s *Source) Load(ctx context.Context, since source.Cursor) (source.Delta, error) {
	op, err := s.begin(ctx)
	if err != nil {
		return source.Delta{}, err
	}
	defer s.end(op)
	if _, err = s.ensureInitialized(op); err != nil {
		return source.Delta{}, err
	}
	if !validCursor(since.Value) {
		return source.Delta{}, source.Refuse("invalid-cursor", "external source cursor is invalid")
	}

	delta := source.Delta{Items: make([]model.Item, 0), Coverage: source.Complete}
	seenItems := make(map[string]struct{})
	seenCursors := make(map[string]struct{})
	cursor := since.Value
	if cursor != "" {
		seenCursors[cursor] = struct{}{}
	}
	for {
		params := map[string]any{
			"sourceId": s.options.ID,
			"cursor":   cursor,
			"maxItems": maxPageItems,
			"maxBytes": resultLimit,
			"deadline": deadlineText(op.ctx),
		}
		raw, _, callErr := s.call(op, "list", params, false)
		if callErr != nil {
			return source.Delta{}, callErr
		}
		page, decodeErr := decodeList(raw, s.options.ID)
		if decodeErr != nil {
			s.fail(op.run, true)
			return source.Delta{}, decodeErr
		}
		for _, item := range page.items {
			if _, exists := seenItems[item.Ref.ID]; exists {
				s.fail(op.run, true)
				return source.Delta{}, source.Refuse("invalid-response", "external source repeated an item reference")
			}
			seenItems[item.Ref.ID] = struct{}{}
			delta.Items = append(delta.Items, item)
		}
		delta.Coverage = combineCoverage(delta.Coverage, page.coverage)
		if page.nextCursor == "" {
			break
		}
		if _, repeated := seenCursors[page.nextCursor]; repeated {
			s.fail(op.run, true)
			return source.Delta{}, source.Refuse("invalid-response", "external source repeated a list cursor")
		}
		seenCursors[page.nextCursor] = struct{}{}
		cursor = page.nextCursor
	}
	return delta, nil
}

func (s *Source) Watch(ctx context.Context, changed func()) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ticker := time.NewTicker(watchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if changed != nil {
				changed()
			}
		}
	}
}

func (s *Source) Read(ctx context.Context, id string) (model.Item, error) {
	if !validItemID(id) {
		return model.Item{}, source.Refuse("invalid-id", "external source item ID is invalid")
	}
	op, err := s.begin(ctx)
	if err != nil {
		return model.Item{}, err
	}
	defer s.end(op)
	if _, err = s.ensureInitialized(op); err != nil {
		return model.Item{}, err
	}
	raw, _, err := s.call(op, "read", map[string]any{
		"sourceId": s.options.ID,
		"id":       id,
		"deadline": deadlineText(op.ctx),
	}, false)
	if err != nil {
		return model.Item{}, err
	}
	item, err := decodeItemResult(raw, s.options.ID, &model.Ref{Source: s.options.ID, ID: id})
	if err != nil {
		s.fail(op.run, true)
	}
	return item, err
}

func (s *Source) Apply(ctx context.Context, mutation model.Mutation) (model.Item, error) {
	if err := s.validateMutation(mutation); err != nil {
		return model.Item{}, err
	}
	op, err := s.begin(ctx)
	if err != nil {
		return model.Item{}, err
	}
	defer s.end(op)
	_, err = s.ensureInitialized(op)
	if err != nil {
		return model.Item{}, err
	}
	if !s.Info().Capabilities.Allows(mutation.Op) {
		return model.Item{}, source.Refuse("unsupported", "external source does not support this operation")
	}
	raw, dispatched, err := s.call(op, "apply", map[string]any{
		"sourceId": s.options.ID,
		"mutation": mutation,
		"deadline": deadlineText(op.ctx),
	}, true)
	if err != nil {
		return model.Item{}, err
	}
	var expected *model.Ref
	if mutation.Op != model.Create {
		expected = &mutation.Ref
	}
	item, err := decodeItemResult(raw, s.options.ID, expected)
	if err != nil {
		s.fail(op.run, true)
		if dispatched {
			return model.Item{}, unknownOutcome()
		}
	}
	return item, err
}

// Prepare supports safe optimistic field edits using the shared source helper.
func (s *Source) Prepare(ctx context.Context, mutation model.Mutation) (source.Prepared, error) {
	if mutation.Op == model.Create || mutation.Ref.Source != s.options.ID || !validItemID(mutation.Ref.ID) || mutation.ExpectedVersion == "" {
		return source.Prepared{}, source.Refuse("conflict", "external edit requires an exact reference and expected version")
	}
	before, err := s.Read(ctx, mutation.Ref.ID)
	if err != nil {
		return source.Prepared{}, err
	}
	if before.Version != mutation.ExpectedVersion {
		return source.Prepared{}, source.Refuse("conflict", "external source version changed")
	}
	prepared, err := source.PrepareFields(before, mutation)
	if err != nil {
		return source.Prepared{}, err
	}
	prepared.Inverse.Ref = mutation.Ref
	return prepared, nil
}

func (s *Source) validateMutation(mutation model.Mutation) error {
	if !knownOperation(mutation.Op) {
		return source.Refuse("unsupported", "external source does not support this operation")
	}
	if mutation.Op == model.Create {
		if mutation.ExpectedVersion != "" || mutation.Ref.ID != "" || (mutation.Ref.Source != "" && mutation.Ref.Source != s.options.ID) || mutation.Item == nil || mutation.Item.Ref.ID != "" || (mutation.Item.Ref.Source != "" && mutation.Item.Ref.Source != s.options.ID) {
			return source.Refuse("invalid-mutation", "external create requires an unallocated item template")
		}
		return nil
	}
	if mutation.Ref.Source != s.options.ID || !validItemID(mutation.Ref.ID) {
		return source.Refuse("source-mismatch", "external mutation reference does not match its source")
	}
	if mutation.ExpectedVersion == "" {
		return source.Refuse("conflict", "external mutation requires an expected version")
	}
	return nil
}

func (s *Source) begin(ctx context.Context) (*operation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case s.gate <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.closedCtx.Done():
		return nil, source.Refuse("closed", "external source is closed")
	}
	if err := ctx.Err(); err != nil {
		<-s.gate
		return nil, err
	}
	baseCtx, cancelBase := context.WithCancel(ctx)
	closeStop := context.AfterFunc(s.closedCtx, cancelBase)
	opCtx, timeoutCancel := context.WithTimeout(baseCtx, s.options.Timeout)
	cancel := func() {
		timeoutCancel()
		closeStop()
		cancelBase()
	}
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		cancel()
		<-s.gate
		return nil, source.Refuse("closed", "external source is closed")
	}
	return &operation{ctx: opCtx, caller: ctx, cancel: cancel}, nil
}

func (s *Source) end(op *operation) {
	if op.timeoutStop != nil {
		op.timeoutStop()
	}
	if op.ctx.Err() != nil || op.caller.Err() != nil {
		if op.run != nil {
			s.fail(op.run, true)
		}
	} else if op.run != nil {
		s.armIdle(op.run)
	}
	op.cancel()
	<-s.gate
}

type operation struct {
	ctx         context.Context
	caller      context.Context
	cancel      context.CancelFunc
	run         *processRun
	timeoutStop func() bool
}

func (s *Source) ensureInitialized(op *operation) (*processRun, error) {
	run, err := s.ensureRun(op)
	if err != nil {
		return nil, err
	}
	if run.initialized.Load() {
		return run, nil
	}
	var majors = []int{protocolMajor}
	raw, _, err := s.call(op, "initialize", map[string]any{"protocolMajors": majors, "sourceId": s.options.ID}, false)
	if err != nil {
		return nil, err
	}
	adapterManifest, err := decodeManifest(raw)
	if err != nil {
		s.fail(run, true)
		return nil, err
	}
	capabilitiesRaw, _, err := s.call(op, "capabilities", map[string]any{
		"sourceId": s.options.ID,
		"deadline": deadlineText(op.ctx),
	}, false)
	if err != nil {
		return nil, err
	}
	caps, err := decodeCapabilities(capabilitiesRaw, adapterManifest.capabilities)
	if err != nil {
		s.fail(run, true)
		return nil, err
	}
	if run.dead.Load() || op.ctx.Err() != nil {
		s.fail(run, true)
		return nil, contextError(op.ctx)
	}
	run.initialized.Store(true)
	s.mu.Lock()
	if s.run != run || s.closed {
		s.mu.Unlock()
		s.fail(run, true)
		return nil, source.Refuse("unavailable", "external adapter initialization was interrupted")
	}
	s.manifest = adapterManifest
	s.capabilities = caps
	s.mu.Unlock()
	return run, nil
}

func (s *Source) ensureRun(op *operation) (*processRun, error) {
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return nil, source.Refuse("closed", "external source is closed")
		}
		if run := s.run; run != nil && !run.dead.Load() {
			if op.run != nil && op.run != run {
				s.mu.Unlock()
				return nil, source.Refuse("unavailable", "external adapter process changed during the operation")
			}
			s.bindOperationLocked(run, op)
			s.mu.Unlock()
			return run, nil
		}
		if op.run != nil {
			s.mu.Unlock()
			return nil, source.Refuse("unavailable", "external adapter process stopped during the operation")
		}
		run := s.run
		retryIn := s.restartAt.Sub(s.now())
		s.mu.Unlock()
		if run != nil {
			select {
			case <-run.reaped:
			case <-op.ctx.Done():
				return nil, op.ctx.Err()
			}
			continue
		}
		if retryIn > 0 {
			timer := time.NewTimer(retryIn)
			select {
			case <-timer.C:
			case <-op.ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return nil, op.ctx.Err()
			}
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return nil, source.Refuse("closed", "external source is closed")
		}
		if s.run != nil {
			s.mu.Unlock()
			continue
		}
		if retryIn = s.restartAt.Sub(s.now()); retryIn > 0 {
			s.mu.Unlock()
			continue
		}
		run, err := s.startLocked()
		if err != nil {
			s.noteStartFailureLocked()
			s.mu.Unlock()
			return nil, source.Refuse("unavailable", "external adapter process could not start")
		}
		s.bindOperationLocked(run, op)
		s.mu.Unlock()
		return run, nil
	}
}

func (s *Source) bindOperationLocked(run *processRun, op *operation) {
	if op.run == run {
		return
	}
	op.run = run
	op.timeoutStop = context.AfterFunc(op.ctx, func() { s.fail(run, true) })
	run.watchID++
	watchID := run.watchID
	if run.callerStop != nil {
		run.callerStop()
	}
	run.callerStop = context.AfterFunc(op.caller, func() { s.expireCaller(run, watchID) })
	if run.idleTimer != nil {
		run.idleTimer.Stop()
		run.idleTimer = nil
		run.idleID++
	}
}

func (s *Source) expireCaller(run *processRun, watchID uint64) {
	s.mu.Lock()
	current := s.run == run && run.watchID == watchID
	s.mu.Unlock()
	if current {
		s.fail(run, true)
	}
}

func (s *Source) armIdle(run *processRun) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.run != run || run.dead.Load() {
		return
	}
	if run.idleTimer != nil {
		run.idleTimer.Stop()
	}
	run.idleID++
	idleID := run.idleID
	run.idleTimer = time.AfterFunc(idleTimeout, func() { s.expireIdle(run, idleID) })
}

func (s *Source) expireIdle(run *processRun, idleID uint64) {
	s.mu.Lock()
	if s.closed || s.run != run || run.dead.Load() || run.idleID != idleID {
		s.mu.Unlock()
		return
	}
	run.idleTimer = nil
	run.expectedExit.Store(true)
	s.trackStoppingLocked(run)
	s.run = nil
	s.capabilities = model.Capabilities{}
	s.manifest = manifest{}
	callerStop := run.callerStop
	run.callerStop = nil
	s.mu.Unlock()
	if callerStop != nil {
		callerStop()
	}
	run.terminate()
}

func (s *Source) fail(run *processRun, restartable bool) {
	if run == nil {
		return
	}
	s.mu.Lock()
	active := s.run == run
	if active {
		if restartable {
			s.noteFailureLocked(run)
		}
		s.trackStoppingLocked(run)
		s.run = nil
		s.capabilities = model.Capabilities{}
		s.manifest = manifest{}
		if run.idleTimer != nil {
			run.idleTimer.Stop()
			run.idleTimer = nil
		}
		run.idleID++
	}
	run.expectedExit.Store(true)
	callerStop := run.callerStop
	run.callerStop = nil
	s.mu.Unlock()
	if callerStop != nil {
		callerStop()
	}
	run.terminate()
	<-run.reaped
}

func (s *Source) trackStoppingLocked(run *processRun) {
	if run.dead.Load() {
		return
	}
	if s.stopping == nil {
		s.stopping = make(map[*processRun]struct{})
	}
	s.stopping[run] = struct{}{}
}

func (s *Source) noteFailureLocked(run *processRun) {
	if !run.failed.CompareAndSwap(false, true) {
		return
	}
	s.restartCount++
	if s.restartCount > 9 {
		s.restartCount = 9
	}
	delay := 100 * time.Millisecond * time.Duration(1<<(s.restartCount-1))
	if delay > 30*time.Second {
		delay = 30 * time.Second
	}
	s.restartAt = s.now().Add(delay)
}

func (s *Source) noteStartFailureLocked() {
	s.restartCount++
	if s.restartCount > 9 {
		s.restartCount = 9
	}
	delay := 100 * time.Millisecond * time.Duration(1<<(s.restartCount-1))
	if delay > 30*time.Second {
		delay = 30 * time.Second
	}
	s.restartAt = s.now().Add(delay)
}

func (s *Source) markHealthy(run *processRun) {
	s.mu.Lock()
	if s.run == run && !run.dead.Load() {
		s.restartCount = 0
		s.restartAt = time.Time{}
	}
	s.mu.Unlock()
}

func (s *Source) call(op *operation, method string, params any, write bool) (json.RawMessage, bool, error) {
	run, err := s.ensureRun(op)
	if err != nil {
		return nil, false, err
	}
	paramsJSON, err := json.Marshal(params)
	if err != nil || !json.Valid(paramsJSON) {
		return nil, false, source.Refuse("invalid-request", "external request parameters are invalid")
	}
	s.mu.Lock()
	s.nextID++
	id := strconv.FormatUint(s.nextID, 10)
	s.mu.Unlock()
	request := struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      string          `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}{"2.0", id, method, paramsJSON}
	frame, err := json.Marshal(request)
	if err != nil || len(frame)+1 > frameLimit {
		return nil, false, source.Refuse("invalid-request", "external request exceeds the frame limit")
	}
	frame = append(frame, '\n')
	if err := op.ctx.Err(); err != nil {
		s.fail(run, true)
		return nil, false, contextError(op.ctx)
	}

	writeResult := make(chan writeOutcome, 1)
	go func() { writeResult <- run.writeFrame(frame) }()
	var dispatched bool
	select {
	case written := <-writeResult:
		dispatched = written.n > 0
		if written.err != nil {
			s.fail(run, true)
			if op.ctx.Err() != nil {
				return nil, dispatched, requestFailure(method, write, dispatched, op.ctx.Err())
			}
			return nil, dispatched, requestFailure(method, write, dispatched, written.err)
		}
	case <-op.ctx.Done():
		s.fail(run, true)
		written := <-writeResult
		dispatched = written.n > 0
		return nil, dispatched, requestFailure(method, write, dispatched, op.ctx.Err())
	case <-run.reaped:
		written := <-writeResult
		dispatched = written.n > 0
		s.fail(run, true)
		return nil, dispatched, requestFailure(method, write, dispatched, fmt.Errorf("adapter process exited"))
	}

	responseFrame, err := readFrame(run.reader)
	if err != nil {
		s.fail(run, true)
		if op.ctx.Err() != nil {
			return nil, dispatched, requestFailure(method, write, dispatched, op.ctx.Err())
		}
		return nil, dispatched, requestFailure(method, write, dispatched, err)
	}
	idResponse, result, rpcErr, err := parseResponse(responseFrame, id)
	_ = idResponse
	if err != nil {
		s.fail(run, true)
		if op.ctx.Err() != nil {
			return nil, dispatched, requestFailure(method, write, dispatched, op.ctx.Err())
		}
		return nil, dispatched, requestFailure(method, write, dispatched, err)
	}
	if rpcErr != nil {
		if rpcErr.code == -32007 {
			return nil, dispatched, source.Refuse("not-found", "external source item was not found")
		}
		return nil, dispatched, source.Refuse("external-error", "external adapter returned a JSON-RPC error")
	}
	if method != "initialize" && method != "capabilities" {
		s.markHealthy(run)
	}
	return result, dispatched, nil
}

func requestFailure(_ string, write, dispatched bool, err error) error {
	if write && dispatched {
		return unknownOutcome()
	}
	var refusal *source.Error
	if errors.As(err, &refusal) && refusal.Reason == "frame" {
		return source.Refuse(refusal.Reason, refusal.Message)
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return source.Refuse("unavailable", "external adapter request failed")
}

func unknownOutcome() error {
	return source.Refuse("unknown-outcome", "external mutation response was lost after dispatch")
}

func contextError(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.Canceled) {
		return context.Canceled
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return source.Refuse("unavailable", "external operation was interrupted")
}

func deadlineText(ctx context.Context) string {
	deadline, ok := ctx.Deadline()
	if !ok {
		return time.Now().Add(defaultTimeout).UTC().Format(time.RFC3339Nano)
	}
	return deadline.UTC().Format(time.RFC3339Nano)
}

func combineCoverage(left, right source.Coverage) source.Coverage {
	if left == source.Unknown || right == source.Unknown {
		return source.Unknown
	}
	if left == source.Partial || right == source.Partial {
		return source.Partial
	}
	return source.Complete
}

func validSourceID(id string) bool {
	return id != "" && len(id) <= 128 && utf8.ValidString(id) && strings.TrimSpace(id) == id && !strings.ContainsAny(id, "\x00\r\n")
}

func validItemID(id string) bool {
	return id != "" && len(id) <= 4096 && utf8.ValidString(id) && strings.TrimSpace(id) != "" && !strings.ContainsAny(id, "\x00\r\n")
}

func validCursor(cursor string) bool {
	return len(cursor) <= 4096 && utf8.ValidString(cursor) && !strings.ContainsRune(cursor, '\x00')
}

func validText(value string) bool {
	return utf8.ValidString(value) && !strings.ContainsRune(value, '\x00')
}

func validVersion(version string) bool {
	return version != "" && strings.TrimSpace(version) != "" && len(version) <= 4096 && validText(version)
}

func validManifestText(text string) bool {
	return text != "" && strings.TrimSpace(text) != "" && len(text) <= 4096 && validText(text)
}

func requireObject(data []byte) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil || object == nil {
		return nil, source.Refuse("invalid-response", "external adapter result must be an object")
	}
	return object, nil
}

func requiredRaw(object map[string]json.RawMessage, key string) (json.RawMessage, error) {
	raw, ok := object[key]
	if !ok || len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, source.Refuse("invalid-response", "external adapter result is missing a required field")
	}
	return raw, nil
}

func decodeManifest(raw json.RawMessage) (manifest, error) {
	var out manifest
	object, err := requireObject(raw)
	if err != nil {
		return out, err
	}
	idRaw, err := requiredRaw(object, "manifest")
	if err != nil {
		return out, err
	}
	var result struct {
		ProtocolVersion int             `json:"protocolVersion"`
		Manifest        json.RawMessage `json:"manifest"`
	}
	if json.Unmarshal(raw, &result) != nil || result.ProtocolVersion != protocolMajor {
		return out, source.Refuse("invalid-response", "external adapter protocol version is unsupported")
	}
	manifestObject, err := requireObject(idRaw)
	if err != nil {
		return out, err
	}
	idField, err := requiredRaw(manifestObject, "id")
	if err != nil {
		return out, err
	}
	versionField, err := requiredRaw(manifestObject, "version")
	if err != nil {
		return out, err
	}
	capabilitiesField, err := requiredRaw(manifestObject, "capabilities")
	if err != nil {
		return out, err
	}
	if json.Unmarshal(idField, &out.id) != nil || !validManifestText(out.id) || json.Unmarshal(versionField, &out.version) != nil || !validManifestText(out.version) {
		return out, source.Refuse("invalid-response", "external adapter manifest ID or version is invalid")
	}
	var capabilities []string
	if capabilitiesField[0] != '[' || json.Unmarshal(capabilitiesField, &capabilities) != nil || capabilities == nil {
		return out, source.Refuse("invalid-response", "external adapter capabilities are invalid")
	}
	out.capabilities = make([]string, 0, len(capabilities))
	seen := make(map[string]struct{}, len(capabilities))
	for _, capability := range capabilities {
		if !validManifestText(capability) {
			return out, source.Refuse("invalid-response", "external adapter capability is invalid")
		}
		if _, duplicate := seen[capability]; duplicate || !knownCapability(capability) {
			return out, source.Refuse("invalid-response", "external adapter declared a duplicate or unsupported capability")
		}
		seen[capability] = struct{}{}
		out.capabilities = append(out.capabilities, capability)
	}
	if _, ok := seen["list"]; !ok {
		return out, source.Refuse("invalid-response", "external adapter capability list is required")
	}
	if _, ok := seen["read"]; !ok {
		return out, source.Refuse("invalid-response", "external adapter capability read is required")
	}
	if required, ok := manifestObject["requiredFeatures"]; ok {
		var features []string
		if len(required) == 0 || required[0] != '[' || json.Unmarshal(required, &features) != nil || features == nil {
			return out, source.Refuse("invalid-response", "external adapter required features are invalid")
		}
		if len(features) != 0 {
			return out, source.Refuse("unsupported-feature", "external adapter requires an unsupported feature")
		}
	}
	return out, nil
}

type manifest struct {
	id           string
	version      string
	capabilities []string
}

func knownCapability(capability string) bool {
	if capability == "list" || capability == "read" {
		return true
	}
	return knownOperation(model.Operation(capability))
}

func decodeCapabilities(raw json.RawMessage, declared []string) (model.Capabilities, error) {
	object, err := requireObject(raw)
	if err != nil {
		return nil, err
	}
	capabilitiesRaw, err := requiredRaw(object, "capabilities")
	if err != nil || capabilitiesRaw[0] != '[' {
		return nil, source.Refuse("invalid-response", "external adapter capabilities result is invalid")
	}
	var got []string
	if json.Unmarshal(capabilitiesRaw, &got) != nil || got == nil {
		return nil, source.Refuse("invalid-response", "external adapter capabilities result is invalid")
	}
	declaredSet := make(map[string]struct{}, len(declared))
	for _, capability := range declared {
		declaredSet[capability] = struct{}{}
	}
	gotSet := make(map[string]struct{}, len(got))
	for _, capability := range got {
		if !knownCapability(capability) {
			return nil, source.Refuse("invalid-response", "external adapter returned an unsupported capability")
		}
		if _, duplicate := gotSet[capability]; duplicate {
			return nil, source.Refuse("invalid-response", "external adapter repeated a capability")
		}
		gotSet[capability] = struct{}{}
	}
	if len(gotSet) != len(declaredSet) {
		return nil, source.Refuse("invalid-response", "external adapter capability response disagrees with its manifest")
	}
	caps := make(model.Capabilities)
	for _, capability := range declared {
		if _, ok := gotSet[capability]; !ok {
			return nil, source.Refuse("invalid-response", "external adapter capability response disagrees with its manifest")
		}
		if capability != "list" && capability != "read" {
			caps[model.Operation(capability)] = true
		}
	}
	return caps, nil
}

func decodeList(raw json.RawMessage, sourceID string) (listPage, error) {
	var page listPage
	object, err := requireObject(raw)
	if err != nil {
		return page, err
	}
	itemsRaw, err := requiredRaw(object, "items")
	if err != nil {
		return page, err
	}
	cursorRaw, err := requiredRaw(object, "nextCursor")
	if err != nil {
		return page, err
	}
	coverageRaw, err := requiredRaw(object, "coverage")
	if err != nil {
		return page, err
	}
	if itemsRaw[0] != '[' || json.Unmarshal(itemsRaw, &page.items) != nil || page.items == nil || len(page.items) > maxPageItems {
		return page, source.Refuse("invalid-response", "external adapter list page exceeds its item limit or is invalid")
	}
	if json.Unmarshal(cursorRaw, &page.nextCursor) != nil || !validCursor(page.nextCursor) {
		return page, source.Refuse("invalid-response", "external adapter list cursor is invalid")
	}
	var coverage string
	if json.Unmarshal(coverageRaw, &coverage) != nil {
		return page, source.Refuse("invalid-response", "external adapter coverage is invalid")
	}
	page.coverage = source.Coverage(coverage)
	if page.coverage != source.Complete && page.coverage != source.Partial && page.coverage != source.Unknown {
		return page, source.Refuse("invalid-response", "external adapter coverage is unsupported")
	}
	for _, item := range page.items {
		if err := validateItem(item, sourceID); err != nil {
			return page, err
		}
	}
	return page, nil
}

type listPage struct {
	items      []model.Item
	nextCursor string
	coverage   source.Coverage
}

func decodeItemResult(raw json.RawMessage, sourceID string, expected *model.Ref) (model.Item, error) {
	var item model.Item
	object, err := requireObject(raw)
	if err != nil {
		return item, err
	}
	itemRaw, err := requiredRaw(object, "item")
	if err != nil || json.Unmarshal(itemRaw, &item) != nil {
		return item, source.Refuse("invalid-response", "external adapter item is invalid")
	}
	if err := validateItem(item, sourceID); err != nil {
		return model.Item{}, err
	}
	if expected != nil && item.Ref != *expected {
		return model.Item{}, source.Refuse("invalid-response", "external adapter returned a different item reference")
	}
	return item, nil
}

func validateItem(item model.Item, sourceID string) error {
	if item.Ref.Source != sourceID || !validItemID(item.Ref.ID) || !validVersion(item.Version) {
		return source.Refuse("invalid-response", "external adapter item reference or version is invalid")
	}
	return nil
}

func knownOperation(operation model.Operation) bool {
	switch operation {
	case model.Batch, model.Create, model.Archive, model.Unarchive, model.Set, model.Add, model.Remove, model.Unset,
		model.ReplaceSection, model.AppendNote, model.AppendComment, model.CheckCriterion, model.UncheckCriterion,
		model.AddCriterion, model.RemoveCriterion, model.SetCriterion, model.Restore:
		return true
	default:
		return false
	}
}

// Check runs a bounded protocol probe and always closes its adapter process.
func Check(ctx context.Context, options Options) error {
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := options.Timeout
	if timeout < 0 {
		return source.Refuse("invalid-source", "external source timeout cannot be negative")
	}
	if timeout == 0 {
		timeout = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	adapter, err := New(options)
	if err != nil {
		return err
	}
	defer adapter.Close()
	if err := adapter.Initialize(ctx); err != nil {
		return err
	}
	delta, err := adapter.Load(ctx, source.Cursor{})
	if err != nil {
		return err
	}
	if len(delta.Items) == 0 {
		return nil
	}
	_, err = adapter.Read(ctx, delta.Items[0].Ref.ID)
	return err
}

// Close terminates and reaps the child, interrupting any active operation.
func (s *Source) Close() error {
	s.mu.Lock()
	if s.closed {
		done := s.closeDone
		s.mu.Unlock()
		<-done
		return nil
	}
	s.closed = true
	run := s.run
	stopping := make([]*processRun, 0, len(s.stopping))
	for pending := range s.stopping {
		stopping = append(stopping, pending)
	}
	closeCancel := s.closeCancel
	s.mu.Unlock()
	closeCancel()
	if run != nil {
		s.fail(run, false)
	}
	for _, pending := range stopping {
		<-pending.reaped
	}
	s.gate <- struct{}{}
	<-s.gate
	close(s.closeDone)
	return nil
}
