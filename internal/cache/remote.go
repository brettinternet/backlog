package cache

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/gob"
	"errors"
	"os"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/source"
)

// RemoteInitial reads at most two batches without taking the refresh lock. WAL
// readers can paint while another process waits on the network. The caller must
// mark these cached rows stale until the provider revalidates them.
func (s Store) RemoteInitial(ctx context.Context) (source.Delta, error) {
	if _, err := os.Stat(s.Path); err != nil {
		return source.Delta{}, err
	}
	db, err := s.open(ctx)
	if err != nil {
		return source.Delta{}, err
	}
	defer db.Close()
	return readRemote(ctx, db, 2)
}

// RemoteRefresh serializes read/network/commit across processes. Rows and cursor
// commit atomically. A failed load never advances the cursor. The refresh lock
// is not a database transaction: warm readers never wait on network I/O.
// RemoteSprints reports whether the cached cursor records sprint demand.
func (s Store) RemoteSprints(ctx context.Context) bool {
	if _, err := os.Stat(s.Path); err != nil {
		return false
	}
	db, err := s.open(ctx)
	if err != nil {
		return false
	}
	defer db.Close()
	delta, err := readRemote(ctx, db, 0)
	return err == nil && delta.Cursor.Sprints
}

func (s Store) RemoteRefresh(ctx context.Context, load func(source.Cursor) (source.Delta, error)) (source.Delta, error) {
	lock, err := s.lock(ctx)
	if err != nil {
		return source.Delta{}, err
	}
	defer unlock(lock)
	db, err := s.open(ctx)
	if err != nil {
		return source.Delta{}, err
	}
	defer db.Close()
	if _, err = db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS remote_state(id INTEGER PRIMARY KEY CHECK(id=1), build TEXT NOT NULL, data BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS remote_rows(batch INTEGER PRIMARY KEY, data BLOB NOT NULL);`); err != nil {
		return source.Delta{}, err
	}
	previous, err := readRemote(ctx, db, -1)
	if err != nil {
		return source.Delta{}, err
	}
	delta, err := load(previous.Cursor)
	if err != nil {
		return source.Delta{}, err
	}
	merged := source.Merge(previous, delta)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return source.Delta{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM remote_rows`); err != nil {
		return source.Delta{}, err
	}
	for start := 0; start < len(merged.Items); start += batchSize {
		batch := source.Delta{Items: merged.Items[start:min(start+batchSize, len(merged.Items))], Stale: map[model.Ref]bool{}, Archived: map[model.Ref]bool{}}
		for _, item := range batch.Items {
			if merged.Stale[item.Ref] {
				batch.Stale[item.Ref] = true
			}
			if merged.Archived[item.Ref] {
				batch.Archived[item.Ref] = true
			}
		}
		var data bytes.Buffer
		if err = gob.NewEncoder(&data).Encode(batch); err != nil {
			return source.Delta{}, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO remote_rows(batch,data) VALUES(?,?)`, start/batchSize, data.Bytes()); err != nil {
			return source.Delta{}, err
		}
	}
	// Metadata carries no item storage, closures, or per-row freshness maps.
	metadata := merged
	metadata.Items, metadata.ImmutableItems, metadata.Stale, metadata.Archived, metadata.TextSearch = nil, nil, nil, nil, nil
	var data bytes.Buffer
	if err = gob.NewEncoder(&data).Encode(metadata); err != nil {
		return source.Delta{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO remote_state(id,build,data) VALUES(1,?,?) ON CONFLICT(id) DO UPDATE SET build=excluded.build,data=excluded.data`, build, data.Bytes()); err != nil {
		return source.Delta{}, err
	}
	if err = tx.Commit(); err != nil {
		return source.Delta{}, err
	}
	return merged, nil
}

// RemoteObserve replaces one cached row with a fresh provider read, or marks it
// stale when item is nil because the provider reports it gone. A boolean
// "archived" field also sets the row's archive flag. It rewrites only
// that row's batch and never moves the cursor, adds rows, or creates a cache.
// It never waits: while a refresh holds the lock it reports false, and that
// refresh's result stands.
func (s Store) RemoteObserve(ctx context.Context, ref model.Ref, item *model.Item) (bool, error) {
	if _, err := os.Stat(s.Path); err != nil {
		return false, nil
	}
	lock, ok, err := s.tryLock()
	if err != nil || !ok {
		return false, err
	}
	defer unlock(lock)
	db, err := s.open(ctx)
	if err != nil {
		return false, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var written string
	err = tx.QueryRowContext(ctx, `SELECT build FROM remote_state WHERE id=1`).Scan(&written)
	if errors.Is(err, sql.ErrNoRows) || err == nil && written != build {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT batch,data FROM remote_rows ORDER BY batch`)
	if err != nil {
		return false, err
	}
	index, found := 0, false
	var batch source.Delta
	for !found && rows.Next() {
		var data []byte
		batch = source.Delta{}
		if err = rows.Scan(&index, &data); err == nil {
			err = gob.NewDecoder(bytes.NewReader(data)).Decode(&batch)
		}
		if err != nil {
			_ = rows.Close()
			return false, err
		}
		for n := range batch.Items {
			if batch.Items[n].Ref != ref {
				continue
			}
			found = true
			if batch.Stale == nil {
				batch.Stale = map[model.Ref]bool{}
			}
			if item == nil {
				batch.Stale[ref] = true
			} else {
				batch.Items[n] = *item
				delete(batch.Stale, ref)
				if archived, ok := item.Fields["archived"].(bool); ok {
					if batch.Archived == nil {
						batch.Archived = map[model.Ref]bool{}
					}
					batch.Archived[ref] = archived
				}
			}
		}
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil || !found {
		return false, err
	}
	var data bytes.Buffer
	if err = gob.NewEncoder(&data).Encode(batch); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE remote_rows SET data=? WHERE batch=?`, data.Bytes(), index); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func readRemote(ctx context.Context, db *sql.DB, limit int) (source.Delta, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return source.Delta{}, err
	}
	defer tx.Rollback()
	var data []byte
	var written string
	err = tx.QueryRowContext(ctx, `SELECT build,data FROM remote_state WHERE id=1`).Scan(&written, &data)
	if errors.Is(err, sql.ErrNoRows) || err == nil && written != build {
		return source.Delta{Coverage: source.Unknown}, nil
	}
	if err != nil {
		return source.Delta{}, err
	}
	var delta source.Delta
	if err = gob.NewDecoder(bytes.NewReader(data)).Decode(&delta); err != nil {
		return source.Delta{}, err
	}
	delta.Stale, delta.Archived = map[model.Ref]bool{}, map[model.Ref]bool{}
	rows, err := tx.QueryContext(ctx, `SELECT data FROM remote_rows ORDER BY batch LIMIT ?`, limit)
	if err != nil {
		return source.Delta{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var data []byte
		var batch source.Delta
		if err = rows.Scan(&data); err == nil {
			err = gob.NewDecoder(bytes.NewReader(data)).Decode(&batch)
		}
		if err != nil {
			return source.Delta{}, err
		}
		delta.Items = append(delta.Items, batch.Items...)
		for ref, value := range batch.Stale {
			delta.Stale[ref] = value
		}
		for ref, value := range batch.Archived {
			delta.Archived[ref] = value
		}
	}
	return delta, rows.Err()
}
