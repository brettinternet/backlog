package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// NewPrincipal places OAuth-authenticated source rows beneath a principal hash
// so explicit logout can purge that principal across projects and source aliases.
func NewPrincipal(root, project, source, path, principal string) Store {
	base := New(root, project, source, path).Path
	relative, err := filepath.Rel(root, base)
	if err != nil || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || relative == ".." {
		return Store{}
	}
	hash := sha256.Sum256([]byte(principal))
	return Store{Path: filepath.Join(root, "principals", hex.EncodeToString(hash[:]), relative)}
}

// PurgePrincipal removes only the disposable cache subtree for one stable
// principal hash. Token storage lives elsewhere and is never removed here.
func PurgePrincipal(ctx context.Context, root, principal string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !filepath.IsAbs(root) || principal == "" {
		return fmt.Errorf("invalid cache principal")
	}
	principals := filepath.Join(root, "principals")
	info, err := os.Lstat(principals)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("unsafe principal cache directory")
	}
	hash := sha256.Sum256([]byte(principal))
	directory := filepath.Join(principals, hex.EncodeToString(hash[:]))
	info, err = os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("unsafe principal cache entry")
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return os.RemoveAll(directory)
}
