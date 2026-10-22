// Package instructions embeds guidance shipped with the executable.
package instructions

import (
	"embed"
	"strings"
)

//go:embed *.md
var topics embed.FS

// Names returns topics in reading order.
func Names() []string { return []string{"overview", "workflow", "query", "format", "json"} }

// Read returns a known topic; caller input never becomes a filesystem path.
func Read(topic string) (string, bool) {
	for _, name := range Names() {
		if topic == name {
			body, err := topics.ReadFile(name + ".md")
			return string(body), err == nil
		}
	}
	return "", false
}

func List() string { return "Topics: " + strings.Join(Names(), ", ") }
