package worklease

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// CompatibleVersion is true from MinimumVersion on. Exact policy
// compatibility is checked by the pinned key vectors.
func CompatibleVersion(version string) bool {
	numbers, ok := parseVersion(version)
	minimum, _ := parseVersion(MinimumVersion)
	if !ok {
		return false
	}
	for i, n := range numbers {
		if n != minimum[i] {
			return n > minimum[i]
		}
	}
	return true
}

func parseVersion(version string) ([3]int, bool) {
	var numbers [3]int
	parts := strings.Split(strings.TrimPrefix(version, "v"), ".")
	if len(parts) != 3 {
		return numbers, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return numbers, false
		}
		numbers[i] = n
	}
	return numbers, true
}

// Probe runs `worklease version --json` in dir and returns the reported
// version once it is schema v2 and at least MinimumVersion.
func Probe(ctx context.Context, dir string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "worklease", "version", "--json")
	command.Dir = dir
	output, err := command.Output()
	if err != nil {
		return "", err
	}
	var v struct {
		OK      bool   `json:"ok"`
		Schema  int    `json:"schemaVersion"`
		Version string `json:"version"`
	}
	if err = json.Unmarshal(output, &v); err != nil {
		return "", err
	}
	if !v.OK || v.Schema != 2 || !CompatibleVersion(v.Version) {
		return v.Version, errors.New("worklease " + v.Version + " is older than " + MinimumVersion + " or lacks schema v2")
	}
	return v.Version, nil
}
