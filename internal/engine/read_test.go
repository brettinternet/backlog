package engine

import (
	"context"
	"testing"

	"github.com/brettinternet/backlog/internal/source"
)

type observationSource struct {
	source.Source
	delta source.Delta
}

func (s observationSource) Info() source.Info { return source.Info{ID: "test"} }
func (s observationSource) Load(context.Context, source.Cursor) (source.Delta, error) {
	return s.delta, nil
}

// A partial scan is not a source failure, but unknown coverage or a malformed
// row must still fail closed instead of authorizing selection from a bad load.
func TestReadObservationCoverage(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		coverage source.Coverage
		findings []source.Finding
		wantErr  bool
	}{
		{name: "complete", coverage: source.Complete},
		{name: "partial", coverage: source.Partial},
		{name: "unknown", coverage: source.Unknown, wantErr: true},
		{name: "unspecified", wantErr: true},
		{name: "invalid-partial", coverage: source.Partial, findings: []source.Finding{{Severity: "error", Code: "invalid-frontmatter"}}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := observationSource{delta: source.Delta{Coverage: tc.coverage, Findings: tc.findings}}
			if _, err := loadObservation(context.Background(), backend, source.Cursor{}); (err != nil) != tc.wantErr {
				t.Fatalf("load error = %v, want error %v", err, tc.wantErr)
			}
		})
	}
}
