// bl-reference-adapter is an ephemeral, read-only external source example.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/source/external"
)

func main() {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	out := json.NewEncoder(os.Stdout)
	initialized := false
	sourceID := ""
	for scanner.Scan() {
		frame := append(append([]byte(nil), scanner.Bytes()...), '\n')
		if external.ValidateFrame(frame) != nil {
			return
		}
		var req struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      string          `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &req) != nil || req.JSONRPC != "2.0" {
			return
		}
		if req.Method == "$/cancelRequest" && req.ID == "" {
			continue
		}
		if req.ID == "" {
			return
		}
		var p struct {
			ProtocolMajors []int  `json:"protocolMajors"`
			SourceID       string `json:"sourceId"`
			ID             string `json:"id"`
			Cursor         string `json:"cursor"`
			MaxItems       int    `json:"maxItems"`
			MaxBytes       int    `json:"maxBytes"`
			Deadline       string `json:"deadline"`
		}
		if json.Unmarshal(req.Params, &p) != nil {
			return
		}
		capabilities := []string{"list", "read"}
		item := model.Item{Ref: model.Ref{Source: sourceID, ID: "REF-1"}, Title: "Reference item", Status: "todo", Category: model.Todo, Version: "1"}
		var result any
		diagnostic := ""
		code := -32602
		switch req.Method {
		case "initialize":
			supported := false
			for _, major := range p.ProtocolMajors {
				supported = supported || major == 1
			}
			if initialized || !supported || p.SourceID == "" {
				diagnostic = "unsupported-capability"
				break
			}
			initialized, sourceID = true, p.SourceID
			result = map[string]any{"protocolVersion": 1, "manifest": map[string]any{"id": "bl.reference", "version": "1.0.0", "capabilities": capabilities, "requiredFeatures": []string{}}}
		case "capabilities":
			result = map[string]any{"capabilities": capabilities}
		case "list":
			if p.Cursor != "" || p.MaxItems < 1 || p.MaxItems > 100 || p.MaxBytes < 1 || p.MaxBytes > 786432 {
				diagnostic = "invalid-params"
				break
			}
			result = map[string]any{"items": []model.Item{item}, "nextCursor": "", "coverage": "complete"}
		case "read":
			if p.ID != item.Ref.ID {
				diagnostic = "not-found"
				code = -32007
				break
			}
			result = map[string]any{"item": item}
		default:
			diagnostic, code = "unsupported-capability", -32601
		}
		if req.Method != "initialize" {
			deadline, err := time.Parse(time.RFC3339Nano, p.Deadline)
			if !initialized || p.SourceID != sourceID || err != nil || !deadline.After(time.Now()) {
				diagnostic = "invalid-params"
			}
		}
		response := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		if diagnostic != "" {
			response["error"] = map[string]any{"code": code, "message": diagnostic, "data": map[string]string{"diagnostic": diagnostic}}
		} else {
			response["result"] = result
		}
		if err := out.Encode(response); err != nil {
			return
		}
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "invalid or oversized request")
	}
}
