package jira

import (
	"encoding/json"
	"testing"
)

// Provider JSON and nested ADF are untrusted. Malformed node types and deeply
// nested documents must not panic while decoding issues or rendering text.
func FuzzIssueJSON(f *testing.F) {
	f.Add([]byte(`{"id":"10001","key":"KAN-1","fields":{"description":{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"café"}]}]}}}`))
	f.Add([]byte(`{"fields":{"description":{"type":1,"content":[null]}}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 8<<20 {
			return
		}
		var row issue
		if json.Unmarshal(data, &row) == nil {
			_ = adfText(row.Fields.Description)
		}
	})
}
