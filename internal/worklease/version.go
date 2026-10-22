package worklease

import (
	"strconv"
	"strings"
)

// CompatibleVersion keeps the v1.9.3 minimum independently of executable lookup.
// Exact policy compatibility is checked by the pinned key vectors.
func CompatibleVersion(version string) bool {
	parts := strings.Split(strings.TrimPrefix(version, "v"), ".")
	if len(parts) != 3 {
		return false
	}
	var numbers [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return false
		}
		numbers[i] = n
	}
	minimum := [3]int{1, 9, 3}
	for i, n := range numbers {
		if n != minimum[i] {
			return n > minimum[i]
		}
	}
	return true
}
