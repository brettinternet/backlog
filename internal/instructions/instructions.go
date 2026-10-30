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

// finish is the workflow sentence tour guidance precedes.
const finish = "Reread the final item, then finish:"

// WithTours asks for a tour of each open change at one of stages in the
// workflow topic under tours mode suggest or require (design.md D43). Any
// other mode leaves the topic unchanged, so it says nothing about tours.
func WithTours(topic, markdown, mode string, stages []string) string {
	if topic != "workflow" || mode != "suggest" && mode != "require" {
		return markdown
	}
	names := make([]string, len(stages))
	for i, stage := range stages {
		names[i] = map[string]string{"pr": "pull request"}[stage]
		if names[i] == "" {
			names[i] = stage
		}
	}
	kinds := strings.Join(names, " or ")
	if len(names) > 2 {
		kinds = strings.Join(names[:len(names)-1], ", ") + ", or " + names[len(names)-1]
	}
	guidance := "Before done, write a tour of each open " + kinds + " linked to the item, at its current head, citing the acceptance criterion each step satisfies, and store it with `bl tour ID FILE --json`; `bl instructions tours` has the format. "
	if mode == "require" {
		guidance += "Done refuses while one lacks a tour for its current head, naming the change; a tour carried forward with no step outdated counts."
	} else {
		guidance += "The done response's tourRequest names any still without one."
	}
	return strings.Replace(markdown, finish, guidance+"\n\n"+finish, 1)
}

func List() string { return "Topics: " + strings.Join(Names(), ", ") }
