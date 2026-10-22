//go:build unix

// Package ptytest drives real terminal children without blocking their output.
package ptytest

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
)

// Session owns a child and a continuously drained, bounded output transcript.
type Session struct {
	cmd      *exec.Cmd
	terminal *os.File
	mu       sync.Mutex
	output   string
	screen   *vt.Emulator
	changed  chan struct{}
	done     chan struct{}
	drained  chan struct{}
	replied  chan struct{}
	err      error
}

func Start(cmd *exec.Cmd, width, height uint16) (*Session, error) {
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: width, Rows: height})
	if err != nil {
		return nil, err
	}
	s := &Session{cmd: cmd, terminal: terminal, screen: vt.NewEmulator(int(width), int(height)), changed: make(chan struct{}, 1), done: make(chan struct{}), drained: make(chan struct{}), replied: make(chan struct{})}
	go func() { defer close(s.replied); _, _ = io.Copy(terminal, s.screen) }()
	go func() {
		defer close(s.drained)
		buf := make([]byte, 8192)
		for {
			n, err := terminal.Read(buf)
			if n > 0 {
				s.mu.Lock()
				s.output += string(buf[:n])
				_, _ = s.screen.Write(buf[:n])
				if len(s.output) > 1<<20 {
					s.output = s.output[len(s.output)-(1<<20):]
				}
				s.mu.Unlock()
				select {
				case s.changed <- struct{}{}:
				default:
				}
			}
			if err != nil {
				return
			}
		}
	}()
	go func() { s.err = cmd.Wait(); close(s.done) }()
	return s, nil
}
func (s *Session) Write(input string) error { _, err := s.terminal.Write([]byte(input)); return err }
func (s *Session) Output() string           { s.mu.Lock(); defer s.mu.Unlock(); return s.output }
func (s *Session) contains(text string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Contains(s.output, text) || strings.Contains(s.screen.String(), text)
}

// Reset starts a new observation window; call after the preceding frame arrived.
func (s *Session) Reset() { s.mu.Lock(); s.output = ""; s.mu.Unlock() }
func (s *Session) WaitFor(text string, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		if s.contains(text) {
			return nil
		}
		select {
		case <-s.changed:
		case <-s.done:
			<-s.drained
			if s.contains(text) {
				return nil
			}
			return fmt.Errorf("child exited (%v), waiting for %q: %q", s.err, text, s.Output())
		case <-timer.C:
			return fmt.Errorf("timeout waiting for %q: %q", text, s.Output())
		}
	}
}

// KeyToOutput measures from write to an observed output marker, not to Update.
// The marker must not already be visible, or the measurement would be zero.
func (s *Session) KeyToOutput(key, marker string, timeout time.Duration) (time.Duration, error) {
	s.Reset()
	if s.contains(marker) {
		return 0, fmt.Errorf("marker %q is already on screen", marker)
	}
	start := time.Now()
	if err := s.Write(key); err != nil {
		return 0, err
	}
	err := s.WaitFor(marker, timeout)
	return time.Since(start), err
}

// Close kills and reaps a still-running child and joins the reader.
func (s *Session) Close() error {
	select {
	case <-s.done:
	default:
		_ = s.cmd.Process.Kill()
		<-s.done
	}
	err := s.terminal.Close()
	// Close the response pipe before joining. Emulator.Close mutates an
	// unsynchronized flag read by Emulator.Read, so call it only after both
	// reader goroutines have exited.
	_ = s.screen.InputPipe().(io.Closer).Close()
	<-s.replied
	<-s.drained
	_ = s.screen.Close()
	return err
}
