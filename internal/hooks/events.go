// Package hooks owns the shared event log and automation event contract.
package hooks

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
	_ "modernc.org/sqlite"
)

type Change struct {
	From   any `json:"from"`
	To     any `json:"to"`
	Reason any `json:"reason,omitempty"`
}

type Event struct {
	SchemaVersion int               `json:"schemaVersion"`
	ID            string            `json:"id"`
	Type          string            `json:"type"`
	Time          time.Time         `json:"time"`
	Project       string            `json:"project"`
	Source        string            `json:"source"`
	Ref           string            `json:"ref"`
	Resource      string            `json:"resource,omitempty"`
	Changes       map[string]Change `json:"changes,omitempty"`
	Item          any               `json:"item,omitempty"`
	Actor         string            `json:"actor,omitempty"`
	Origin        string            `json:"origin"`
	Cause         *string           `json:"cause"`
	Depth         int               `json:"depth"`
	Versions      []string          `json:"versions"`
	Name          string            `json:"name,omitempty"`
	Outcome       string            `json:"outcome,omitempty"`
	ExitCode      *int              `json:"exitCode,omitempty"`
	Duration      time.Duration     `json:"duration,omitempty"`
	Result        map[string]any    `json:"result,omitempty"`
}

// Log retains rotated segments: cursors and dedupe identities never silently
// expire. Removing project state explicitly discards replay history.
type Log struct {
	Dir      string
	MaxBytes int64
}

func New(stateDir, project string) Log {
	key := sha256.Sum256([]byte(project))
	return Log{Dir: filepath.Join(stateDir, "events", fmt.Sprintf("%x", key)), MaxBytes: 8 << 20}
}

type Transaction struct {
	log        Log
	db         *sql.Tx
	ctx        context.Context
	connection *sql.DB
	appended   []Event
}

// Update serializes producers and readers, including their cache publication.
func (l Log) Update(ctx context.Context, fn func(*Transaction) error) error {
	if err := os.MkdirAll(l.Dir, 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(l.Dir, "lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	for {
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Millisecond * 5):
		}
	}
	defer unix.Flock(int(f.Fd()), unix.LOCK_UN)
	tx := &Transaction{log: l, ctx: ctx}
	defer func() {
		if tx.db != nil {
			tx.db.Rollback()
		}
		if tx.connection != nil {
			tx.connection.Close()
		}
	}()
	if err = fn(tx); err != nil {
		return err
	}
	if tx.db != nil {
		return tx.db.Commit()
	}
	return nil
}

// Appended returns only events owned by this producer, including their IDs.
func (tx *Transaction) Appended() []Event { return append([]Event(nil), tx.appended...) }
func (tx *Transaction) ensureIndex() error {
	if tx.db != nil {
		return nil
	}
	db, err := tx.log.openIndex(tx.ctx)
	if err != nil {
		return err
	}
	tx.connection = db
	tx.db, err = db.BeginTx(tx.ctx, nil)
	if err != nil {
		return err
	}
	return tx.syncIndex()
}
func identity(e Event) string {
	b, _ := json.Marshal([]any{e.Type, e.Ref, e.Versions})
	return string(b)
}

// The SQLite index is disposable. JSONL remains the authority: under the log
// lock every transaction first indexes new complete lines, including appends
// whose producer died before publishing its index/cache transaction.
func (l Log) openIndex(ctx context.Context) (*sql.DB, error) {
	path := filepath.Join(l.Dir, "index.sqlite")
	for attempt := 0; attempt < 2; attempt++ {
		db, err := sql.Open("sqlite", path)
		if err != nil {
			return nil, err
		}
		db.SetMaxOpenConns(1)
		_, err = db.ExecContext(ctx, `PRAGMA journal_mode=WAL; PRAGMA synchronous=NORMAL; CREATE TABLE IF NOT EXISTS events(id TEXT PRIMARY KEY, identity TEXT NOT NULL UNIQUE, data BLOB NOT NULL); CREATE TABLE IF NOT EXISTS files(path TEXT PRIMARY KEY, size INTEGER NOT NULL, modified INTEGER NOT NULL);`)
		if err == nil {
			return db, nil
		}
		db.Close()
		if attempt > 0 || !corruptIndex(err) {
			return nil, err
		}
		for _, suffix := range []string{"", "-wal", "-shm"} {
			if e := os.Remove(path + suffix); e != nil && !errors.Is(e, os.ErrNotExist) {
				return nil, e
			}
		}
	}
	return nil, fmt.Errorf("cannot open event index")
}
func corruptIndex(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "malformed") || strings.Contains(message, "not a database")
}
func (tx *Transaction) syncIndex() error {
	paths, err := filepath.Glob(filepath.Join(tx.log.Dir, "*.jsonl"))
	if err != nil {
		return err
	}
	sort.Strings(paths)
	type stamp struct{ size, modified int64 }
	known := map[string]stamp{}
	rows, err := tx.db.QueryContext(tx.ctx, `SELECT path,size,modified FROM files`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var path string
		var s stamp
		if err = rows.Scan(&path, &s.size, &s.modified); err != nil {
			rows.Close()
			return err
		}
		known[path] = s
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	present := map[string]bool{}
	for _, path := range paths {
		present[path] = true
	}
	rebuild := false
	for path := range known {
		if !present[path] && filepath.Base(path) != "current.jsonl" {
			rebuild = true
		}
	}
	if rebuild {
		if _, err = tx.db.ExecContext(tx.ctx, `DELETE FROM events; DELETE FROM files`); err != nil {
			return err
		}
		known = map[string]stamp{}
	}
	for _, path := range paths {
		if filepath.Base(path) == "current.jsonl" {
			if err = repairTail(path); err != nil {
				return err
			}
		}
		info, e := os.Stat(path)
		if e != nil {
			return e
		}
		prior := known[path]
		if prior.size == info.Size() && prior.modified == info.ModTime().UnixNano() {
			continue
		}
		offset := prior.size
		if offset > info.Size() || prior.size == info.Size() {
			offset = 0
		}
		f, e := os.Open(path)
		if e != nil {
			return e
		}
		if _, e = f.Seek(offset, io.SeekStart); e != nil {
			f.Close()
			return e
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 4096), 16<<20)
		for scanner.Scan() {
			var event Event
			data := append([]byte(nil), scanner.Bytes()...)
			if e = json.Unmarshal(data, &event); e != nil {
				f.Close()
				return fmt.Errorf("event log %s: %w", path, e)
			}
			if _, e = tx.db.ExecContext(tx.ctx, `INSERT OR IGNORE INTO events(id,identity,data) VALUES(?,?,?)`, event.ID, identity(event), data); e != nil {
				f.Close()
				return e
			}
		}
		e = scanner.Err()
		f.Close()
		if e != nil {
			return e
		}
		if _, err = tx.db.ExecContext(tx.ctx, `INSERT OR REPLACE INTO files(path,size,modified) VALUES(?,?,?)`, path, info.Size(), info.ModTime().UnixNano()); err != nil {
			return err
		}
	}
	return nil
}

// Append returns true only to the producer that owns dispatch of this event.
func (tx *Transaction) Append(e Event) (bool, error) {
	if err := tx.ensureIndex(); err != nil {
		return false, err
	}
	key := identity(e)
	var exists int
	if err := tx.db.QueryRowContext(tx.ctx, `SELECT count(*) FROM events WHERE identity=?`, key).Scan(&exists); err != nil {
		return false, err
	}
	if exists > 0 {
		return false, nil
	}
	if e.Depth < 0 {
		return false, fmt.Errorf("negative event depth")
	}
	e.SchemaVersion = 1
	e.Time = time.Now().UTC()
	var last string
	if err := tx.db.QueryRowContext(tx.ctx, `SELECT coalesce(max(id),'') FROM events`).Scan(&last); err != nil {
		return false, err
	}
	e.ID = newID(e.Time, last)
	data, err := json.Marshal(e)
	if err != nil {
		return false, err
	}
	data = append(data, '\n')
	if len(data) > 16<<20 {
		return false, fmt.Errorf("event exceeds 16 MiB")
	}
	path := filepath.Join(tx.log.Dir, "current.jsonl")
	if info, err := os.Stat(path); err == nil && tx.log.MaxBytes > 0 && info.Size() > 0 && info.Size()+int64(len(data)) > tx.log.MaxBytes {
		if err = os.Rename(path, filepath.Join(tx.log.Dir, last+".jsonl")); err != nil {
			return false, err
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return false, err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return false, err
	}
	if closeErr != nil {
		return false, closeErr
	}
	dir, err := os.Open(tx.log.Dir)
	if err != nil {
		return false, err
	}
	err = dir.Sync()
	dir.Close()
	if err != nil {
		return false, err
	}
	if _, err = tx.db.ExecContext(tx.ctx, `INSERT INTO events(id,identity,data) VALUES(?,?,?)`, e.ID, key, data); err != nil {
		return false, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	if _, err = tx.db.ExecContext(tx.ctx, `INSERT OR REPLACE INTO files(path,size,modified) VALUES(?,?,?)`, path, info.Size(), info.ModTime().UnixNano()); err != nil {
		return false, err
	}
	tx.appended = append(tx.appended, e)
	return true, nil
}

// Event reads one complete event by ID from the durable log.
func (l Log) Event(ctx context.Context, id string) (Event, error) {
	var event Event
	err := l.Update(ctx, func(tx *Transaction) error {
		if err := tx.ensureIndex(); err != nil {
			return err
		}
		var data []byte
		if err := tx.db.QueryRowContext(ctx, `SELECT data FROM events WHERE id=?`, id).Scan(&data); err != nil {
			return fmt.Errorf("unknown event %q: %w", id, err)
		}
		return json.Unmarshal(data, &event)
	})
	return event, err
}

func (l Log) Read(ctx context.Context, since string) ([]Event, error) {
	var result []Event
	err := l.Update(ctx, func(tx *Transaction) error {
		if err := tx.ensureIndex(); err != nil {
			return err
		}
		if since != "" {
			var exists int
			if err := tx.db.QueryRowContext(ctx, `SELECT count(*) FROM events WHERE id=?`, since).Scan(&exists); err != nil {
				return err
			}
			if exists == 0 {
				return fmt.Errorf("unknown event cursor %q", since)
			}
		}
		rows, err := tx.db.QueryContext(ctx, `SELECT data FROM events WHERE id>? ORDER BY id`, since)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var data []byte
			if err = rows.Scan(&data); err != nil {
				return err
			}
			var e Event
			if err = json.Unmarshal(data, &e); err != nil {
				return err
			}
			result = append(result, e)
		}
		return rows.Err()
	})
	return result, err
}

// An interrupted append is not an event. Discard only the unterminated tail;
// corruption in a completed line remains an explicit error.
func repairTail(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	end := info.Size()
	if end == 0 {
		return nil
	}
	var last [1]byte
	if _, err = f.ReadAt(last[:], end-1); err != nil {
		return err
	}
	if last[0] == '\n' {
		return nil
	}
	for end > 0 {
		start := max(int64(0), end-4096)
		chunk := make([]byte, end-start)
		if _, err = f.ReadAt(chunk, start); err != nil {
			return err
		}
		if n := bytes.LastIndexByte(chunk, '\n'); n >= 0 {
			if err = f.Truncate(start + int64(n) + 1); err != nil {
				return err
			}
			return f.Sync()
		}
		end = start
	}
	if err = f.Truncate(0); err != nil {
		return err
	}
	return f.Sync()
}

const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

func newID(now time.Time, last string) string {
	var b [16]byte
	_, err := rand.Read(b[6:])
	if err != nil {
		panic(err)
	}
	ms := uint64(now.UnixMilli())
	for n := 5; n >= 0; n-- {
		b[n] = byte(ms)
		ms >>= 8
	}
	value := new(big.Int).SetBytes(b[:])
	text := encodeID(value)
	if text <= last {
		value.SetInt64(0)
		for _, c := range last {
			value.Lsh(value, 5)
			value.Add(value, big.NewInt(int64(strings.IndexRune(alphabet, c))))
		}
		value.Add(value, big.NewInt(1))
		text = encodeID(value)
	}
	return text
}
func encodeID(value *big.Int) string {
	var out [26]byte
	v := new(big.Int).Set(value)
	mask := big.NewInt(31)
	for n := 25; n >= 0; n-- {
		out[n] = alphabet[new(big.Int).And(v, mask).Int64()]
		v.Rsh(v, 5)
	}
	return string(out[:])
}
