package cli_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/engine"
	blmcp "github.com/brettinternet/backlog/internal/mcp"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func nextFake(t *testing.T, root string) string {
	t.Helper()
	bin := t.TempDir()
	path := filepath.Join(bin, "worklease")
	put(t, path, get(t, "testdata/next-worklease.py"))
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(t.TempDir(), "state.json")
	t.Setenv("BL_FAKE_ROOT", root)
	t.Setenv("BL_FAKE_STATE", state)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return state
}
func nextItems(t *testing.T, root string) {
	t.Helper()
	put(t, filepath.Join(root, "items/BL-013.md"), strings.ReplaceAll(get(t, filepath.Join(root, "items/BL-012.md")), "BL-012", "BL-013"))
}
func nextClient(t *testing.T, root string) (context.Context, *sdk.ClientSession) {
	t.Helper()
	paths, err := project.UserPaths()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := project.Resolve(paths, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	st, ct := sdk.NewInMemoryTransports()
	done := make(chan error, 1)
	go func() { done <- blmcp.New(cfg).Run(ctx, st) }()
	client, err := sdk.NewClient(&sdk.Implementation{Name: "claimed-loop", Version: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close(); cancel(); <-done })
	return ctx, client
}

// A session is a hard authorization boundary: neither transport may consult
// Worklease or inherit an MCP server's ambient caller when it is omitted.
func TestNextClaimRequiresSessionAcrossTransports(t *testing.T) {
	root := writeFixture(t)
	enableCoordination(t, root)
	state := nextFake(t, root)
	t.Setenv("BL_SESSION", "")
	code, out, _ := writeRun(root, "", "next", "--claim")
	if code != 2 || !strings.Contains(out, `"reason":"session-required"`) {
		t.Fatalf("CLI missing session: %d %s", code, out)
	}
	t.Setenv("BL_SESSION", "ambient-server-session")
	ctx, client := nextClient(t, root)
	result, err := client.CallTool(ctx, &sdk.CallToolParams{Name: "next", Arguments: map[string]any{"claim": true}})
	if err != nil || !result.IsError {
		t.Fatalf("MCP missing session: %+v %v", result, err)
	}
	b, _ := json.Marshal(result.StructuredContent)
	if !strings.Contains(string(b), `"reason":"session-required"`) {
		t.Fatalf("MCP missing reason: %s", b)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatalf("Worklease called without session: %v", err)
	}
	code, out, _ = writeRun(root, "", "next", "--start")
	if code != 2 || !strings.Contains(out, "requires --claim") {
		t.Fatalf("ungated start: %d %s", code, out)
	}
}

// The fake's barrier forces both transports to try the same first item; the
// authority records no second held claim, and asserts todo at every new grant.
func TestNextClaimConcurrentCLIAndMCP(t *testing.T) { testNextClaimConcurrentCLIAndMCP(t, false) }
func TestProviderNextClaimConcurrentCLIAndMCP(t *testing.T) {
	if os.Getenv("BL_PROVIDER_TESTS") != "1" {
		t.Skip("set BL_PROVIDER_TESTS=1 for Worklease 1.10.1")
	}
	testNextClaimConcurrentCLIAndMCP(t, true)
}
func testNextClaimConcurrentCLIAndMCP(t *testing.T, real bool) {
	providerBinary := os.Getenv("BL_PROVIDER_WORKLEASE")
	root := writeFixture(t)
	enableCoordination(t, root)
	nextItems(t, root)
	state := ""
	if real {
		selectProviderBinary(t, providerBinary)
		assertProviderBuild(t)
	} else {
		state = nextFake(t, root)
		t.Setenv("BL_FAKE_BARRIER", "1")
	}
	ctx, client := nextClient(t, root)
	type cliResult struct {
		code        int
		out, stderr string
	}
	cliDone := make(chan cliResult, 1)
	go func() {
		code, out, stderr := writeRun(root, "", "--session", "cli-loop", "next", "--claim", "--start", "--explain")
		cliDone <- cliResult{code, out, stderr}
	}()
	mcpResult, err := client.CallTool(ctx, &sdk.CallToolParams{Name: "next", Arguments: map[string]any{"claim": true, "start": true, "session": "mcp-loop", "explain": true}})
	cliResultValue := <-cliDone
	if err != nil || mcpResult.IsError || cliResultValue.code != 0 {
		t.Fatalf("concurrent calls: CLI %+v MCP %+v %v", cliResultValue, mcpResult, err)
	}
	var cliDecoded struct {
		Data struct {
			Items []struct{ ID, Status string }
			engine.NextOutcome
			Explanations []engine.NextExplanation
		}
	}
	if err := json.Unmarshal([]byte(cliResultValue.out), &cliDecoded); err != nil {
		t.Fatal(err)
	}
	var mcpDecoded blmcp.NextClaimResult
	b, _ := json.Marshal(mcpResult.StructuredContent)
	if err := json.Unmarshal(b, &mcpDecoded); err != nil {
		t.Fatal(err)
	}
	if len(cliDecoded.Data.Items) != 1 || len(mcpDecoded.Items) != 1 || cliDecoded.Data.Items[0].ID == mcpDecoded.Items[0].ID {
		t.Fatalf("same or missing item: %s %s", cliResultValue.out, b)
	}
	if cliDecoded.Data.Status != "written" || mcpDecoded.Status != "written" || cliDecoded.Data.Claim.Outcome != model.ClaimAcquired || mcpDecoded.Claim.Outcome != model.ClaimAcquired {
		t.Fatalf("separate outcomes: %s %s", cliResultValue.out, b)
	}
	if len(cliDecoded.Data.Explanations) != 2 || len(mcpDecoded.Explanations) == 0 {
		t.Fatalf("explain ignored: %s %s", cliResultValue.out, b)
	}
	if !real {
		var ledger struct {
			Calls []struct {
				Op, Session string
				Argv        []string
			}
			Claims map[string]model.ClaimInfo
		}
		if err := json.Unmarshal([]byte(get(t, state)), &ledger); err != nil {
			t.Fatal(err)
		}
		acquires := 0
		for _, call := range ledger.Calls {
			if call.Op == "acquire" {
				acquires++
			}
		}
		if acquires != 3 || len(ledger.Claims) != 2 {
			t.Fatalf("walk did not skip contention once: %s", get(t, state))
		}
	}
	// Each caller closes its own item. If done returned before release, reuse
	// would fail claim-held or resume the just-closed item.
	code, out, stderr := writeRun(root, "", "--session", "cli-loop", "done", cliDecoded.Data.Items[0].ID)
	if code != 0 || !strings.Contains(out, `"outcome":"released"`) {
		t.Fatalf("done: %d %s %s", code, out, stderr)
	}
	result, err := client.CallTool(ctx, &sdk.CallToolParams{Name: "update", Arguments: map[string]any{"id": mcpDecoded.Items[0].ID, "session": "mcp-loop", "fields": map[string]any{"status": "done"}}})
	if err != nil || result.IsError {
		t.Fatalf("MCP done: %+v %v", result, err)
	}
}

// A lost acquire reply survives process/request restart. Only the bare retained
// handle replay can confirm it; still-unknown replay must not try item two.
func TestNextClaimPendingRestart(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "confirmed", true: "still-unknown"}[unknown], func(t *testing.T) {
			root := writeFixture(t)
			enableCoordination(t, root)
			nextItems(t, root)
			state := nextFake(t, root)
			t.Setenv("BL_FAKE_PENDING", "1")
			code, out, _ := writeRun(root, "", "next", "--claim")
			if code != 6 || !strings.Contains(out, `"reason":"unknown-outcome"`) || !strings.Contains(out, `"id":"BL-012"`) {
				t.Fatalf("lost acquire: %d %s", code, out)
			}
			if unknown {
				t.Setenv("BL_FAKE_REPLAY_UNKNOWN", "1")
			}
			code, out, _ = writeRun(root, "", "next", "--claim")
			if unknown {
				if code != 6 || !strings.Contains(out, `"resumed":false`) || !strings.Contains(out, `"reason":"unknown-outcome"`) {
					t.Fatalf("uncertain resume: %d %s", code, out)
				}
			} else {
				if code != 0 || !strings.Contains(out, `"resumed":true`) || !strings.Contains(out, `"id":"BL-012"`) {
					t.Fatalf("confirmed resume: %d %s", code, out)
				}
				code, out, _ = writeRun(root, "", "next", "--claim")
				if code != 0 || !strings.Contains(out, `"outcome":"held"`) || !strings.Contains(out, `"resumed":true`) {
					t.Fatalf("held resume: %d %s", code, out)
				}
			}
			var ledger struct {
				Calls []struct {
					Op   string
					Argv []string
				}
			}
			if err := json.Unmarshal([]byte(get(t, state)), &ledger); err != nil {
				t.Fatal(err)
			}
			acquires := 0
			for _, call := range ledger.Calls {
				if call.Op == "acquire" {
					acquires++
				}
			}
			if acquires != 2 {
				t.Fatalf("pending restarted walk: %s", get(t, state))
			}
		})
	}
}

func TestNextClaimDoneThenNextCLI(t *testing.T) {
	root := writeFixture(t)
	enableCoordination(t, root)
	nextItems(t, root)
	nextFake(t, root)
	for _, operation := range []struct {
		args []string
		want string
	}{
		{[]string{"next", "--claim", "--start"}, `"id":"BL-012"`},
		{[]string{"done", "12"}, `"outcome":"released"`},
		{[]string{"next", "--claim"}, `"id":"BL-013"`},
	} {
		code, out, stderr := writeRun(root, "", operation.args...)
		if code != 0 || !strings.Contains(out, operation.want) {
			t.Fatalf("%v: %d %s %s", operation.args, code, out, stderr)
		}
	}
}
