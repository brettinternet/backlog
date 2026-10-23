package oauth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const storeVersion = 1
const maxRecordSize = 32 << 10

// Store keeps token records under an owner-private state directory. Each
// account has its own flock file so unrelated credentials never serialize.
type Store struct{ StateDir string }

type storedRecord struct {
	Version           int       `json:"version"`
	Provider          string    `json:"provider"`
	Account           string    `json:"account"`
	Host              string    `json:"host"`
	User              string    `json:"user"`
	PrincipalID       string    `json:"principalId"`
	AccessToken       string    `json:"accessToken"`
	RefreshToken      string    `json:"refreshToken,omitempty"`
	ExpiresAt         time.Time `json:"expiresAt,omitempty"`
	Scopes            []string  `json:"scopes"`
	ValidationPending bool      `json:"validationPending,omitempty"`
}

func (storedRecord) String() string   { return "[REDACTED]" }
func (storedRecord) GoString() string { return "[REDACTED]" }

func tokenKey(account Account) string {
	return PrincipalKey(account.Provider, account.Host, account.Name)
}
func fileID(key string) string {
	hash := sha256.Sum256([]byte(key))
	return hex.EncodeToString(hash[:])
}

func (s Store) directory() (string, error) {
	if !filepath.IsAbs(s.StateDir) || s.StateDir == string(filepath.Separator) {
		return "", fmt.Errorf("invalid state directory")
	}
	if err := secureDirectory(s.StateDir); err != nil {
		return "", err
	}
	dir := filepath.Join(s.StateDir, "oauth")
	if err := secureDirectory(dir); err != nil {
		return "", err
	}
	return dir, nil
}

func secureDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("unsafe OAuth directory")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("OAuth directory is accessible by other users")
	}
	return nil
}

func (s Store) withRecord(ctx context.Context, key string, update func(*storedRecord, func(*storedRecord) error) (*storedRecord, bool, error)) error {
	if update == nil {
		return fmt.Errorf("invalid OAuth store operation")
	}
	dir, err := s.directory()
	if err != nil {
		return err
	}
	id := fileID(key)
	lockPath := filepath.Join(dir, id+".lock")
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	if err = lock.Chmod(0o600); err != nil {
		lock.Close()
		return err
	}
	if err = lockRecord(ctx, lock); err != nil {
		lock.Close()
		return err
	}
	defer func() {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
	}()

	path := filepath.Join(dir, id+".json")
	current, err := readRecord(path)
	if err != nil {
		return err
	}
	persist := func(record *storedRecord) error {
		if record == nil {
			return removeRecord(path, dir)
		}
		return writeRecord(path, dir, *record)
	}
	next, changed, callbackErr := update(current, persist)
	if changed {
		err = persist(next)
		if err != nil {
			return err
		}
	}
	return callbackErr
}

func lockRecord(ctx context.Context, file *os.File) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			return err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}

func readRecord(path string) (*storedRecord, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > maxRecordSize {
		return nil, fmt.Errorf("unsafe OAuth token record")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxRecordSize+1))
	if err != nil || len(data) > maxRecordSize {
		return nil, fmt.Errorf("unreadable OAuth token record")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var record storedRecord
	if err = decoder.Decode(&record); err != nil {
		return nil, fmt.Errorf("invalid OAuth token record")
	}
	if decoder.Decode(new(any)) != io.EOF || record.Version != storeVersion || !validToken(record.AccessToken) || !validToken(record.RefreshToken) || record.AccessToken == "" || record.Provider == "" || record.Account == "" || record.User == "" || record.PrincipalID == "" || len(record.Scopes) > 128 {
		return nil, fmt.Errorf("invalid OAuth token record")
	}
	return &record, nil
}

func writeRecord(path, dir string, record storedRecord) error {
	if record.Version != storeVersion || !validToken(record.AccessToken) || !validToken(record.RefreshToken) || record.AccessToken == "" || record.Provider == "" || record.Account == "" || record.User == "" || record.PrincipalID == "" || len(record.Scopes) > 128 {
		return fmt.Errorf("invalid OAuth token record")
	}
	data, err := json.Marshal(record)
	if err != nil || len(data) > maxRecordSize {
		return fmt.Errorf("OAuth token record exceeds limit")
	}
	file, err := os.CreateTemp(dir, ".oauth-*.tmp")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if err = file.Chmod(0o600); err == nil {
		_, err = file.Write(data)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(temporary, path); err != nil {
		return err
	}
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func removeRecord(path, dir string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func recordFor(account Account, principal Principal, tokens Tokens) storedRecord {
	return storedRecord{
		Version: storeVersion, Provider: account.Provider, Account: account.Name,
		Host: account.Host, User: account.User, PrincipalID: principal.ID,
		AccessToken: tokens.AccessToken, RefreshToken: tokens.RefreshToken,
		ExpiresAt: tokens.ExpiresAt, Scopes: ScopeSet(principal.Scopes), ValidationPending: true,
	}
}

func recordMatches(account Account, record *storedRecord) bool {
	return record != nil && record.Provider == account.Provider && record.Account == account.Name && strings.EqualFold(record.Host, account.Host) && record.User == account.User
}

func tokensFrom(record *storedRecord) Tokens {
	return Tokens{AccessToken: record.AccessToken, RefreshToken: record.RefreshToken, ExpiresAt: record.ExpiresAt}
}
