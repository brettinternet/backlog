package external

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/brettinternet/backlog/internal/source"
)

type processRun struct {
	cmd           *exec.Cmd
	stdin         *os.File
	stdout        *os.File
	reader        *bufio.Reader
	reaped        chan struct{}
	dead          atomic.Bool
	expectedExit  atomic.Bool
	failed        atomic.Bool
	initialized   atomic.Bool
	terminateOnce sync.Once
	callerStop    func() bool
	watchID       uint64
	idleTimer     *time.Timer
	idleID        uint64
}

type writeOutcome struct {
	n   int
	err error
}

type rpcError struct {
	code int
}

func (s *Source) startLocked() (*processRun, error) {
	stdinRead, stdinWrite, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	stdoutRead, stdoutWrite, err := os.Pipe()
	if err != nil {
		_ = stdinRead.Close()
		_ = stdinWrite.Close()
		return nil, err
	}
	cmd := exec.Command(s.options.Run[0], s.options.Run[1:]...)
	cmd.Dir = s.options.Dir
	cmd.Env = adapterEnvironment()
	cmd.Stdin = stdinRead
	cmd.Stdout = stdoutWrite
	cmd.Stderr = io.Discard
	prepareProcess(cmd)
	if err = cmd.Start(); err != nil {
		_ = stdinRead.Close()
		_ = stdinWrite.Close()
		_ = stdoutRead.Close()
		_ = stdoutWrite.Close()
		return nil, err
	}
	_ = stdinRead.Close()
	_ = stdoutWrite.Close()
	run := &processRun{
		cmd:    cmd,
		stdin:  stdinWrite,
		stdout: stdoutRead,
		reader: bufio.NewReaderSize(stdoutRead, frameLimit),
		reaped: make(chan struct{}),
	}
	s.run = run
	go s.reap(run)
	return run, nil
}

func (s *Source) reap(run *processRun) {
	_ = run.cmd.Wait()
	run.dead.Store(true)
	s.mu.Lock()
	delete(s.stopping, run)
	if s.run == run {
		s.run = nil
		s.capabilities = nil
		s.manifest = manifest{}
		if run.idleTimer != nil {
			run.idleTimer.Stop()
			run.idleTimer = nil
		}
		run.idleID++
		if !run.expectedExit.Load() && !s.closed {
			s.noteFailureLocked(run)
		}
	}
	callerStop := run.callerStop
	run.callerStop = nil
	s.mu.Unlock()
	if callerStop != nil {
		callerStop()
	}
	close(run.reaped)
}

func (r *processRun) terminate() {
	r.terminateOnce.Do(func() {
		terminateProcess(r.cmd)
		_ = r.stdin.Close()
		_ = r.stdout.Close()
	})
}

func (r *processRun) writeFrame(frame []byte) writeOutcome {
	written := 0
	for len(frame) > 0 {
		n, err := r.stdin.Write(frame)
		written += n
		frame = frame[n:]
		if err != nil {
			return writeOutcome{n: written, err: err}
		}
		if n == 0 {
			return writeOutcome{n: written, err: io.ErrShortWrite}
		}
	}
	return writeOutcome{n: written}
}

func readFrame(reader *bufio.Reader) ([]byte, error) {
	frame, err := reader.ReadSlice('\n')
	if err != nil {
		if errors.Is(err, bufio.ErrBufferFull) {
			return nil, source.Refuse("frame", "external adapter response frame exceeds the protocol limit")
		}
		if errors.Is(err, io.EOF) && len(frame) != 0 {
			return nil, io.ErrUnexpectedEOF
		}
		return nil, err
	}
	if len(frame) > frameLimit {
		return nil, source.Refuse("frame", "external adapter response frame exceeds the protocol limit")
	}
	if err := ValidateFrame(frame); err != nil {
		return nil, source.Refuse("frame", "external adapter response frame is invalid")
	}
	return append([]byte(nil), frame...), nil
}

// ValidateFrame checks the compact JSON object framing used by the external
// adapter protocol. The frame limit includes its terminating LF.
func ValidateFrame(frame []byte) error {
	if len(frame) < 3 || len(frame) > frameLimit || frame[len(frame)-1] != '\n' {
		return fmt.Errorf("invalid external adapter frame length or terminator")
	}
	data := frame[:len(frame)-1]
	if !utf8.Valid(data) || bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) {
		return fmt.Errorf("external adapter frame is not valid UTF-8 JSON")
	}
	var compact bytes.Buffer
	if json.Compact(&compact, data) != nil || !bytes.Equal(compact.Bytes(), data) {
		return fmt.Errorf("external adapter frame is not compact JSON")
	}
	if err := rejectDuplicateKeys(data); err != nil {
		return fmt.Errorf("external adapter frame has duplicate keys or invalid JSON")
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(data, &object) != nil || object == nil {
		return fmt.Errorf("external adapter frame must contain a JSON object")
	}
	return nil
}

func parseResponse(frame []byte, expectedID string) (string, json.RawMessage, *rpcError, error) {
	if err := ValidateFrame(frame); err != nil {
		return "", nil, nil, err
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(frame[:len(frame)-1], &object) != nil {
		return "", nil, nil, fmt.Errorf("invalid JSON-RPC response")
	}
	for key := range object {
		if key != "jsonrpc" && key != "id" && key != "result" && key != "error" {
			return "", nil, nil, fmt.Errorf("unknown JSON-RPC response field")
		}
	}
	var version, id string
	if json.Unmarshal(object["jsonrpc"], &version) != nil || version != "2.0" ||
		json.Unmarshal(object["id"], &id) != nil || id == "" || id != expectedID {
		return "", nil, nil, fmt.Errorf("invalid JSON-RPC response identity")
	}
	result, hasResult := object["result"]
	errorRaw, hasError := object["error"]
	if hasResult == hasError {
		return "", nil, nil, fmt.Errorf("JSON-RPC response must contain exactly one result or error")
	}
	if hasResult {
		if len(result) == 0 || bytes.Equal(result, []byte("null")) || len(result) > resultLimit {
			return "", nil, nil, fmt.Errorf("JSON-RPC result exceeds the protocol limit or is null")
		}
		var resultObject map[string]json.RawMessage
		if json.Unmarshal(result, &resultObject) != nil || resultObject == nil {
			return "", nil, nil, fmt.Errorf("JSON-RPC result must be an object")
		}
		return id, append(json.RawMessage(nil), result...), nil, nil
	}
	var errorObject map[string]json.RawMessage
	if json.Unmarshal(errorRaw, &errorObject) != nil || errorObject == nil {
		return "", nil, nil, fmt.Errorf("invalid JSON-RPC error object")
	}
	for key := range errorObject {
		if key != "code" && key != "message" && key != "data" {
			return "", nil, nil, fmt.Errorf("invalid JSON-RPC error field")
		}
	}
	var code int
	var message string
	if json.Unmarshal(errorObject["code"], &code) != nil || json.Unmarshal(errorObject["message"], &message) != nil || message == "" || len(message) > 4096 {
		return "", nil, nil, fmt.Errorf("invalid JSON-RPC error object")
	}
	var data map[string]json.RawMessage
	var diagnostic string
	if json.Unmarshal(errorObject["data"], &data) != nil || data == nil || json.Unmarshal(data["diagnostic"], &diagnostic) != nil || diagnostic == "" || len(diagnostic) > 4096 {
		return "", nil, nil, source.Refuse("frame", "invalid JSON-RPC error diagnostic")
	}
	return id, nil, &rpcError{code: code}, nil
}

func rejectDuplicateKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := consumeJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return fmt.Errorf("trailing JSON value")
	}
	return nil
}

func consumeJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("invalid JSON object key")
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("duplicate JSON object key")
			}
			seen[key] = struct{}{}
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return fmt.Errorf("invalid JSON object")
		}
	case '[':
		for decoder.More() {
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return fmt.Errorf("invalid JSON array")
		}
	default:
		return fmt.Errorf("invalid JSON delimiter")
	}
	return nil
}

func adapterEnvironment() []string {
	allowed := map[string]struct{}{
		"PATH": {}, "HOME": {}, "USER": {}, "LOGNAME": {}, "LANG": {}, "TMPDIR": {},
		"LC_ALL": {}, "LC_COLLATE": {}, "LC_CTYPE": {}, "LC_MESSAGES": {}, "LC_MONETARY": {},
		"LC_NUMERIC": {}, "LC_TIME": {}, "XDG_CONFIG_HOME": {}, "XDG_CACHE_HOME": {},
		"XDG_DATA_HOME": {}, "XDG_STATE_HOME": {},
	}
	values := make(map[string]string, len(allowed))
	for _, entry := range os.Environ() {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || strings.HasPrefix(name, "PI_") || strings.HasPrefix(name, "WORKLEASE_") || name == "GH_TOKEN" || name == "GITHUB_TOKEN" {
			continue
		}
		if _, ok := allowed[name]; ok {
			values[name] = value
		}
	}
	keys := make([]string, 0, len(values))
	for name := range values {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, name := range keys {
		out = append(out, name+"="+values[name])
	}
	return out
}
