// Package instructions embeds guidance shipped with the executable.
package instructions

import (
	"embed"
	"strings"
)

//go:embed *.md
var topics embed.FS

// Names returns topics in reading order.
func Names() []string { return []string{"overview", "workflow", "query", "format", "json", "tours"} }

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

// completion is the workflow sentence a commit trailer line follows.
const completion = "blocked; do not mark incomplete work done."

// WithCommitTrailer asks for a KEY: ID trailer on each commit for an item in
// the workflow topic, the only commit guidance bl gives (design.md D40). An
// empty key leaves the topic unchanged.
func WithCommitTrailer(topic, markdown, key string) string {
	if topic != "workflow" || key == "" {
		return markdown
	}
	return strings.Replace(markdown, completion, completion+"\n\nEnd each commit for an item with a `"+key+": ID` trailer.", 1)
}

func List() string { return "Topics: " + strings.Join(Names(), ", ") }
