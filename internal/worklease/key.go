// Package worklease implements coordination using the Worklease v1.10.1 CLI.
package worklease

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/source/jira"
)

// Key mirrors Worklease's portable policies, including ASCII JSON hashing.
func Key(provider, source, item string) (string, error) {
	for _, value := range []string{source, item} {
		if !utf8.ValidString(value) || strings.TrimSpace(value) == "" || len(value) > 1024 {
			return "", fmt.Errorf("invalid resource identity")
		}
		for _, r := range value {
			if r < 32 || r == 127 {
				return "", fmt.Errorf("invalid resource identity")
			}
		}
	}
	var key string
	switch provider {
	case "github":
		source = strings.TrimSuffix(strings.ToLower(source), ".git")
		if source == "" {
			return "", fmt.Errorf("invalid source")
		}
		encode := func(s string) string { return strings.ReplaceAll(url.QueryEscape(s), "+", "%20") }
		key = "github:" + encode(source) + "#" + encode(item)
	case "generic", "linear":
		data := `{"item":` + asciiQuote(item) + `,"provider":` + asciiQuote(provider) + `,"source":` + asciiQuote(source) + `}`
		sum := sha256.Sum256([]byte(data))
		key = "coordination:" + provider + ":" + hex.EncodeToString(sum[:])
	default:
		return "", fmt.Errorf("unsupported key policy %q", provider)
	}
	if len(key) > 1024 {
		return "", fmt.Errorf("derived resource exceeds 1024 bytes")
	}
	return key, nil
}
func asciiQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		default:
			if r > 126 {
				// v1.9.3 hashes one \\u prefix for a surrogate pair. Preserve
				// those exact bytes rather than silently fixing the upstream policy.
				b.WriteString(`\u`)
				for _, u := range utf16.Encode([]rune{r}) {
					fmt.Fprintf(&b, `%04x`, u)
				}
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// projectScoped reports whether the item's key derives from the project ID.
func projectScoped(cfg project.Config, item model.Item) bool {
	for _, s := range cfg.Sources {
		if s.ID == item.Ref.Source {
			return s.Type == "native" || s.Type == "backlog-md"
		}
	}
	return false
}
func resource(cfg project.Config, item model.Item) (string, error) {
	for _, s := range cfg.Sources {
		if s.ID != item.Ref.Source {
			continue
		}
		switch s.Type {
		case "github", "github-pulls":
			host := s.Host
			if host == "" {
				host = "github.com"
			}
			// A project-only source's OWNER/REPO#N IDs key as that repository's
			// source would, so both sources claim the same issue.
			if s.Repo == "" {
				repo, number, ok := strings.Cut(item.Ref.ID, "#")
				if !ok || strings.Count(repo, "/") != 1 {
					return "", fmt.Errorf("github issue ID %s is not OWNER/REPO#N; drafts can't be claimed", strconv.Quote(item.Ref.ID))
				}
				return Key("github", host+"/"+repo, number)
			}
			// A Projects v2 draft has no issue number, so no key: promote it first.
			number := strings.TrimPrefix(item.Ref.ID, "#")
			if n, err := strconv.Atoi(number); err != nil || n < 1 || strconv.Itoa(n) != number {
				return "", fmt.Errorf("github item %s is not an issue number; drafts can't be claimed", strconv.Quote(item.Ref.ID))
			}
			return Key("github", host+"/"+s.Repo, number)
		case "linear":
			stable, _ := item.Fields["uuid"].(string)
			if stable == "" {
				return "", fmt.Errorf("linear stable UUID unavailable")
			}
			return Key("linear", s.Site, stable)
		case "jira":
			site, err := jira.CanonicalSite(s.Site)
			if err != nil {
				return "", err
			}
			stable, _ := item.Fields["id"].(string)
			if stable == "" {
				return "", fmt.Errorf("jira numeric identity unavailable")
			}
			return Key("generic", site, stable)
		case "native", "backlog-md":
			return Key("generic", cfg.Project.ID, item.Ref.ID)
		default:
			stable, _ := item.Fields["id"].(string)
			if stable == "" {
				return "", fmt.Errorf("stable provider identity unavailable for %s", strconv.Quote(s.ID))
			}
			return Key("generic", s.Site, stable)
		}
	}
	return "", fmt.Errorf("unknown source %s", item.Ref.Source)
}
