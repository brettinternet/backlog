package native

import (
	"context"
	"path/filepath"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/source"
)

// Prepare uses the same lossless codec as Apply. Apply still checks the version
// under the write lock: preparation never grants authority to overwrite a race.
func (s *Source) Prepare(ctx context.Context, m model.Mutation) (source.Prepared, error) {
	if err := ctx.Err(); err != nil {
		return source.Prepared{}, err
	}
	path, err := s.locate(ctx, m.Ref.ID)
	if err != nil {
		return source.Prepared{}, err
	}
	doc, err := s.document(path)
	if err != nil {
		return source.Prepared{}, err
	}
	if m.ExpectedVersion == "" || m.ExpectedVersion != doc.Version() {
		return source.Prepared{}, source.Refuse("conflict", "expected version does not match current content")
	}
	if m.Op == model.Archive || m.Op == model.Unarchive {
		inverse := model.Archive
		if filepath.Dir(path) == s.options.Path {
			inverse = model.Unarchive
		}
		return source.Prepared{Item: s.item(doc), Inverse: model.Mutation{Ref: m.Ref, Op: inverse}}, nil
	}
	m.Ref.ID = doc.Item().Ref.ID
	next, inverse, err := doc.Apply(m, s.options.Now())
	if err != nil {
		return source.Prepared{}, err
	}
	inverse.Ref.Source = s.options.ID
	return source.Prepared{Item: s.item(next), Inverse: inverse}, nil
}
