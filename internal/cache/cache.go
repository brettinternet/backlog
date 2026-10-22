// Package cache provides disposable, stat-validated native item snapshots.
package cache

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/gob"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/query"
	_ "modernc.org/sqlite"
)

const schema = 3

// build identifies the executable that wrote cached rows. Parsed items are
// opaque gob values, so a parser or model change in a new binary must discard
// them; an unknown executable never matches and always reparses.
var build = func() string {
	path, err := os.Executable()
	if err == nil {
		var info os.FileInfo
		if info, err = os.Stat(path); err == nil {
			return fmt.Sprintf("%s:%d:%d", path, info.Size(), info.ModTime().UnixNano())
		}
	}
	return rand.Text()
}()

func init() {
	gob.Register(map[string]any{})
	gob.Register([]any{})
	gob.Register(time.Time{})
}

type Record struct {
	Path string
	Item *model.Item
	Err  error // failed parses are deliberately not cached
}

type Snapshot struct {
	Records    []Record
	Generation string
}

type Store struct{ Path string }

// New separates sources and checkout paths as well as project identities.
func New(root, project, source, path string) Store {
	return Store{filepath.Join(root, "projects", fmt.Sprintf("%x", sha256.Sum256([]byte(project))), fmt.Sprintf("%x.sqlite", sha256.Sum256([]byte(source+"\x00"+path))))}
}

// FindPaths returns files whose names map to key. It persists a directory-name
// index so detail lookups avoid reading every item directory on each invocation.
// identify receives a filename and returns its normalized key when it is an item.
func (s Store) FindPaths(ctx context.Context, directories []string, key string, identify func(string) (string, bool)) ([]string, error) {
	lock, err := s.lock(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock(lock)
	db, err := s.open(ctx)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	identities := make(map[string]string, len(directories))
	for _, dir := range directories {
		if _, exists := identities[dir]; exists {
			continue
		}
		identity, e := directoryIdentity(dir)
		if e != nil {
			return nil, e
		}
		identities[dir] = identity
	}
	indexed := make(map[string]string, len(directories))
	rows, err := tx.QueryContext(ctx, `SELECT path,identity FROM id_directories`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var path, identity string
		if err = rows.Scan(&path, &identity); err != nil {
			_ = rows.Close()
			return nil, err
		}
		indexed[path] = identity
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	stale := len(indexed) != len(identities)
	if !stale {
		for dir, identity := range identities {
			if indexed[dir] != identity {
				stale = true
				break
			}
		}
	}
	if stale {
		stable := false
		for attempt := 0; attempt < 2; attempt++ {
			if _, err = tx.ExecContext(ctx, `DELETE FROM id_items; DELETE FROM id_directories;`); err != nil {
				return nil, err
			}
			changed := false
			for dir, identity := range identities {
				entries, e := os.ReadDir(dir)
				if e != nil && !errors.Is(e, os.ErrNotExist) {
					return nil, e
				}
				for _, entry := range entries {
					if err = ctx.Err(); err != nil {
						return nil, err
					}
					if !entry.Type().IsRegular() {
						continue
					}
					id, ok := identify(entry.Name())
					if !ok {
						continue
					}
					if _, err = tx.ExecContext(ctx, `INSERT INTO id_items(id,path) VALUES(?,?)`, id, filepath.Join(dir, entry.Name())); err != nil {
						return nil, err
					}
				}
				current, e := directoryIdentity(dir)
				if e != nil {
					return nil, e
				}
				if current != identity {
					changed = true
					break
				}
			}
			if !changed {
				for dir, identity := range identities {
					if _, err = tx.ExecContext(ctx, `INSERT INTO id_directories(path,identity) VALUES(?,?)`, dir, identity); err != nil {
						return nil, err
					}
				}
				stable = true
				break
			}
			if attempt == 1 {
				return nil, fmt.Errorf("item directory changed while indexing")
			}
			identities = make(map[string]string, len(directories))
			for _, dir := range directories {
				if _, exists := identities[dir]; exists {
					continue
				}
				identity, e := directoryIdentity(dir)
				if e != nil {
					return nil, e
				}
				identities[dir] = identity
			}
		}
		if !stable {
			return nil, fmt.Errorf("item directory changed while indexing")
		}
	}
	rows, err = tx.QueryContext(ctx, `SELECT path FROM id_items WHERE id=? ORDER BY path`, key)
	if err != nil {
		return nil, err
	}
	paths := []string{}
	for rows.Next() {
		var path string
		if err = rows.Scan(&path); err != nil {
			_ = rows.Close()
			return nil, err
		}
		paths = append(paths, path)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return paths, nil
}

func (s Store) lock(ctx context.Context) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(s.Path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	for {
		if err = ctx.Err(); err != nil {
			_ = f.Close()
			return nil, err
		}
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			_ = f.Close()
			return nil, err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}
func unlock(f *os.File) { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }

func (s Store) open(ctx context.Context) (*sql.DB, error) {
	db, err := sql.Open("sqlite", s.Path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	fail := func(err error) (*sql.DB, error) { _ = db.Close(); return nil, err }
	if _, err = db.ExecContext(ctx, `PRAGMA busy_timeout=5000; PRAGMA journal_mode=WAL;`); err != nil {
		return fail(err)
	}
	var version int
	if err = db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return fail(err)
	}
	if version != 0 && version != 2 && version != schema {
		return fail(fmt.Errorf("cache schema %d, expected %d", version, schema))
	}
	if version == 0 {
		_, err = db.ExecContext(ctx, `CREATE TABLE items(path TEXT PRIMARY KEY, identity TEXT NOT NULL, data BLOB NOT NULL);
CREATE VIRTUAL TABLE search USING fts5(path UNINDEXED, text, tokenize='trigram case_sensitive 1');
CREATE TABLE metadata(generation TEXT NOT NULL, build TEXT NOT NULL);
INSERT INTO metadata VALUES ('','');
CREATE TABLE id_directories(path TEXT PRIMARY KEY, identity TEXT NOT NULL);
CREATE TABLE id_items(id TEXT NOT NULL, path TEXT NOT NULL, PRIMARY KEY(id,path));
PRAGMA user_version=3;`)
		if err != nil {
			return fail(err)
		}
	} else if version == 2 {
		_, err = db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS id_directories(path TEXT PRIMARY KEY, identity TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS id_items(id TEXT NOT NULL, path TEXT NOT NULL, PRIMARY KEY(id,path));
PRAGMA user_version=3;`)
		if err != nil {
			return fail(err)
		}
	}
	return db, nil
}

// Refresh holds the source lock from directory enumeration through commit. A
// second process therefore observes the first process's newly parsed rows.
// covered defines the paths this view can delete (e.g. excluding archives).
// Errors are cache failures: the caller must fall back to authoritative files.
func (s Store) Refresh(ctx context.Context, paths func() ([]string, error), covered func(string) bool, parse func(string) (model.Item, error)) (Snapshot, []string, error) {
	lock, err := s.lock(ctx)
	if err != nil {
		return Snapshot{}, nil, err
	}
	defer unlock(lock)
	// Enumeration failures are source errors, not cache damage.
	files, err := paths()
	if err != nil {
		return Snapshot{}, nil, err
	}
	var warnings []string
	for attempt := 0; attempt < 2; attempt++ {
		db, e := s.open(ctx)
		if e == nil {
			var snapshot Snapshot
			snapshot, e = refresh(ctx, db, files, covered, parse)
			_ = db.Close()
			if e == nil {
				return snapshot, warnings, nil
			}
		}
		err = e
		if ctx.Err() != nil {
			break
		}
		warnings = append(warnings, "rebuilding disposable cache: "+e.Error())
		// Discard rather than reuse a damaged database or its WAL sidecars. The
		// cache is disposable, and kept copies would grow without bound.
		for _, extra := range []string{"", "-wal", "-shm"} {
			if e := os.Remove(s.Path + extra); e != nil && !errors.Is(e, os.ErrNotExist) {
				return Snapshot{}, warnings, e
			}
		}
	}
	return Snapshot{}, warnings, err
}

// Bounded gob batches amortize type metadata without holding the entire encoded
// snapshot in Go and SQLite buffers alongside its decoded items.
const batchSize = 128

type cachedRecord struct {
	Path, Identity string
	Item           model.Item
}

func refresh(ctx context.Context, db *sql.DB, files []string, covered func(string) bool, parse func(string) (model.Item, error)) (Snapshot, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, err
	}
	defer tx.Rollback()
	var written string
	if err = tx.QueryRowContext(ctx, `SELECT build FROM metadata`).Scan(&written); err != nil {
		return Snapshot{}, err
	}
	changed := written != build
	var records []*cachedRecord
	if changed {
		if _, err = tx.ExecContext(ctx, `DELETE FROM items; DELETE FROM search;`); err != nil {
			return Snapshot{}, err
		}
	} else {
		rows, err := tx.QueryContext(ctx, `SELECT data FROM items ORDER BY path`)
		if err != nil {
			return Snapshot{}, err
		}
		for rows.Next() {
			var data []byte
			var batch []*cachedRecord
			if err = rows.Scan(&data); err == nil {
				err = gob.NewDecoder(bytes.NewReader(data)).Decode(&batch)
			}
			if err != nil {
				_ = rows.Close()
				return Snapshot{}, err
			}
			records = append(records, batch...)
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return Snapshot{}, err
		}
	}
	old := make(map[string]*cachedRecord, len(records))
	for _, r := range records {
		old[r.Path] = r
	}
	records = make([]*cachedRecord, 0, len(old))
	snapshot := Snapshot{Generation: rand.Text(), Records: make([]Record, 0, len(files))}
	for _, path := range files {
		if err = ctx.Err(); err != nil {
			return Snapshot{}, err
		}
		r, exists := old[path]
		delete(old, path)
		identity, e := fileIdentity(path)
		var item model.Item
		if exists {
			item = r.Item
		}
		if e == nil && (!exists || r.Identity != identity) {
			item, e = parse(path)
			changed = true
			if e == nil {
				after, statErr := fileIdentity(path)
				// Never associate old content with a newer stat identity.
				if statErr != nil || after != identity {
					identity = ""
				}
			}
		}
		if e != nil {
			changed = true
			snapshot.Records = append(snapshot.Records, Record{Path: path, Err: e})
			if _, err = tx.ExecContext(ctx, `DELETE FROM search WHERE path=?`, path); err != nil {
				return Snapshot{}, err
			}
			continue
		}
		if !exists || r.Identity != identity {
			if _, err = tx.ExecContext(ctx, `DELETE FROM search WHERE path=?`, path); err != nil {
				return Snapshot{}, err
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO search(path,text) VALUES(?,?)`, path, strings.ToLower(query.SearchText(item))); err != nil {
				return Snapshot{}, err
			}
		}
		if !exists || r.Identity != identity {
			r = &cachedRecord{Path: path, Identity: identity, Item: item}
		}
		records = append(records, r)
		snapshot.Records = append(snapshot.Records, Record{Path: path, Item: &r.Item})
	}
	for path, r := range old {
		// A partial native view must not evict cached archive rows.
		if !covered(path) {
			records = append(records, r)
			continue
		}
		changed = true
		if _, err = tx.ExecContext(ctx, `DELETE FROM search WHERE path=?`, path); err != nil {
			return Snapshot{}, err
		}
	}
	if changed {
		if _, err = tx.ExecContext(ctx, `DELETE FROM items`); err != nil {
			return Snapshot{}, err
		}
		for start := 0; start < len(records); start += batchSize {
			var data bytes.Buffer
			if err = gob.NewEncoder(&data).Encode(records[start:min(start+batchSize, len(records))]); err != nil {
				return Snapshot{}, err
			}
			// Keys identify batches, not files. Build identity invalidates older
			// encodings before they can reach this decoder.
			if _, err = tx.ExecContext(ctx, `INSERT INTO items VALUES(?,'',?)`, fmt.Sprint(start), data.Bytes()); err != nil {
				return Snapshot{}, err
			}
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE metadata SET generation=?,build=?`, snapshot.Generation, build); err != nil {
		return Snapshot{}, err
	}
	return snapshot, tx.Commit()
}

// Search returns candidate paths from precisely this snapshot's generation.
// Short strings and NULs cannot safely use the trigram index; nil means fallback.
func (s Store) Search(ctx context.Context, generation, text string) (map[string]bool, error) {
	text = strings.ToLower(text)
	if utf8.RuneCountInString(text) < 3 || strings.ContainsRune(text, 0) {
		return nil, nil
	}
	db, err := sql.Open("sqlite", s.Path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err = db.ExecContext(ctx, `PRAGMA busy_timeout=5000`); err != nil {
		return nil, err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var current string
	if err = tx.QueryRowContext(ctx, `SELECT generation FROM metadata`).Scan(&current); err != nil {
		return nil, err
	}
	if current != generation {
		return nil, fmt.Errorf("cache snapshot changed")
	}
	rows, err := tx.QueryContext(ctx, `SELECT path FROM search WHERE search MATCH ?`, `"`+strings.ReplaceAll(text, `"`, `""`)+`"`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	matches := map[string]bool{}
	for rows.Next() {
		var path string
		if err = rows.Scan(&path); err != nil {
			return nil, err
		}
		matches[path] = true
	}
	return matches, rows.Err()
}
