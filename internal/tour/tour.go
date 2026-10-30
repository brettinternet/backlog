// Package tour reads, validates, and stores the CodeTour files agents write
// to guide a reader through one linked change (design.md D43). bl renders
// tours and never generates them.
package tour

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/brettinternet/backlog/internal/project"
)

const (
	// MaxBytes bounds a tour file.
	MaxBytes = 1 << 20
	// MaxSteps bounds a tour's steps.
	MaxSteps = 200
	// maxDescription bounds one step's description.
	maxDescription = 16 << 10
	maxTitle       = 1 << 10
	maxPath        = 4 << 10
)

// SideOld marks a step on a deleted line; a step without a side is on the
// new side.
const SideOld = "old"

// Tour is a CodeTour .tour file: Ref is the head commit it was written for,
// which picks the change it attaches to.
type Tour struct {
	Schema      string `json:"$schema,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Ref         string `json:"ref"`
	IsPrimary   bool   `json:"isPrimary,omitempty"`
	NextTour    string `json:"nextTour,omitempty"`
	Steps       []Step `json:"steps"`
}

// Step is one stop: a line of a file, on the new side unless Side is old,
// with a Markdown description and, optionally, the acceptance criterion it
// shows met, by its one-based index.
type Step struct {
	File        string `json:"file"`
	Line        int    `json:"line"`
	Description string `json:"description"`
	Title       string `json:"title,omitempty"`
	Criterion   int    `json:"criterion,omitempty"`
	Side        string `json:"side,omitempty"`
}

// Stored is a tour as bl keeps it: the item and change it attaches to.
type Stored struct {
	Item   string    `json:"item"`
	Change string    `json:"change"`
	Stored time.Time `json:"stored"`
	Tour   Tour      `json:"tour"`
}

// Parse decodes and validates a tour, refusing unknown keys so a misspelled
// extension doesn't silently drop out.
func Parse(data []byte) (Tour, error) {
	var t Tour
	if len(data) > MaxBytes {
		return t, errors.New("tour exceeds 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&t); err != nil {
		return Tour{}, fmt.Errorf("tour is not valid CodeTour JSON: %w", err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		return Tour{}, errors.New("tour holds more than one JSON value")
	}
	return t, t.validate()
}

func (t Tour) validate() error {
	if !isSHA(t.Ref) {
		return errors.New("tour ref must be the full head commit SHA")
	}
	if len(t.Title) > maxTitle {
		return errors.New("tour title exceeds 1 KiB")
	}
	if len(t.Description) > maxDescription {
		return errors.New("tour description exceeds 16 KiB")
	}
	if len(t.Steps) == 0 {
		return errors.New("tour has no steps")
	}
	if len(t.Steps) > MaxSteps {
		return fmt.Errorf("tour has more than %d steps", MaxSteps)
	}
	for i, s := range t.Steps {
		if err := s.validate(); err != nil {
			return fmt.Errorf("tour step %d: %w", i+1, err)
		}
	}
	return nil
}

func (s Step) validate() error {
	switch {
	case !validPath(s.File):
		return errors.New("file must be a relative path inside the repository, with / separators")
	case s.Line < 1:
		return errors.New("line must be a positive line number")
	case strings.TrimSpace(s.Description) == "":
		return errors.New("description is empty")
	case len(s.Description) > maxDescription:
		return errors.New("description exceeds 16 KiB")
	case len(s.Title) > maxTitle:
		return errors.New("title exceeds 1 KiB")
	case s.Criterion < 0:
		return errors.New("criterion must be a one-based acceptance criterion index")
	case s.Side != "" && s.Side != SideOld && s.Side != "new":
		return errors.New(`side must be "old" or "new"`)
	}
	return nil
}

// validPath is a clean, relative, slash-separated path with no .. and no
// control characters, so it can't name a file outside the repository.
func validPath(p string) bool {
	if p == "" || len(p) > maxPath || !utf8.ValidString(p) || strings.ContainsRune(p, '\\') {
		return false
	}
	for _, r := range p {
		if unicode.IsControl(r) {
			return false
		}
	}
	return path.Clean(p) == p && !path.IsAbs(p) && p != "." && p != ".." && !strings.HasPrefix(p, "../")
}

func isSHA(ref string) bool {
	if len(ref) != 40 && len(ref) != 64 {
		return false
	}
	for _, r := range ref {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

// Path is where the tour for one change of an item is kept in the state
// directory.
func Path(stateDir, projectID, item, change string) string {
	key := sha256.Sum256([]byte(projectID + "\x00" + item + "\x00" + change))
	return filepath.Join(stateDir, "tours", fmt.Sprintf("%x.json", key))
}

// Store keeps s for its item and change, replacing any earlier tour.
func Store(stateDir, projectID string, s Stored) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return project.WriteStateFile(Path(stateDir, projectID, s.Item, s.Change), data)
}

// Load reads the tour kept for an item's change; ok is false when there is
// none.
func Load(stateDir, projectID, item, change string) (Stored, bool, error) {
	var s Stored
	data, err := readFile(Path(stateDir, projectID, item, change), MaxBytes+4<<10)
	if errors.Is(err, os.ErrNotExist) {
		return s, false, nil
	}
	if err != nil {
		return s, false, err
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return s, false, fmt.Errorf("stored tour: %w", err)
	}
	return s, true, s.Tour.validate()
}

// ReadFile reads a tour file an agent wrote: a regular file of at most
// MaxBytes.
func ReadFile(name string) ([]byte, error) {
	return readFile(name, MaxBytes)
}

func readFile(name string, limit int64) ([]byte, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("tour is not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("tour exceeds 1 MiB")
	}
	return data, nil
}
