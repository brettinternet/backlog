// demo runs the real TUI over BL-12 fixtures, never a user's project.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/brettinternet/backlog/internal/fixtures"
)

func main() {
	if err := record(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func record() error {
	binary, err := filepath.Abs("bin/bl")
	if err != nil {
		return err
	}
	root, err := os.MkdirTemp("", "bl-demo-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	project := filepath.Join(root, "project")
	if err := fixtures.Generate(project, 48, 1); err != nil {
		return err
	}
	config := filepath.Join(project, "backlog.yaml")
	file, err := os.OpenFile(config, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := file.WriteString("project: {id: bl-demo, name: Demo}\n")
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	cmd := exec.Command(binary, "-C", project, "tui")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	for _, entry := range os.Environ() {
		key := strings.SplitN(entry, "=", 2)[0]
		if strings.HasPrefix(key, "BL_") || strings.HasPrefix(key, "WORKLEASE_") ||
			strings.HasPrefix(key, "XDG_") || key == "HOME" || key == "USERPROFILE" ||
			key == "NO_COLOR" || key == "COLORFGBG" {
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	cmd.Env = append(cmd.Env, "HOME="+root, "USERPROFILE="+root,
		"XDG_CONFIG_HOME="+filepath.Join(root, "config"),
		"XDG_STATE_HOME="+filepath.Join(root, "state"),
		"XDG_CACHE_HOME="+filepath.Join(root, "cache"),
		"TERM=xterm-256color", "COLORTERM=truecolor", "TZ=UTC")
	return cmd.Run()
}
