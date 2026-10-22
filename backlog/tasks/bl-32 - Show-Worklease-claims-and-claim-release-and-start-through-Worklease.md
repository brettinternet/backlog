---
id: BL-32
title: Add the coordination seam and Worklease claims
status: To Do
assignee: []
created_date: '2026-10-22 00:50'
updated_date: '2026-10-22 02:14'
labels:
  - worklease
  - agents
  - tui
milestone: m-2
dependencies:
  - BL-8
  - BL-15
  - BL-16
  - BL-27
  - BL-28
references:
  - docs/design.md
  - 'https://github.com/brettinternet/worklease/blob/v1.9.3/docs/claim-model.md'
modified_files:
  - internal/model
  - internal/engine
  - internal/worklease
  - internal/cli
  - internal/tui
  - internal/mcp
  - internal/query
  - docs/cli.md
  - docs/automation.md
  - docs/design.md
  - docs/mcp.md
priority: medium
type: feature
ordinal: 32000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
## Why

Parallel agents need to see and take claims where they pick work (D20, §9). `bl` knows about claims, not about Worklease. Claims are a typed engine input, because readiness, `next --claim`, and claim-gated status transitions branch on outcomes that exit-code hooks cannot express.

## What

- A coordination seam in `internal/model` and `internal/engine`: per-item claim state, plus acquire, release, and list with the outcomes `acquired`, `contended` (with holder), `uncertain`, and `unavailable`, and the `{resource}` value per item. No public protocol yet (D18).
- `internal/worklease`, the only implementation:
  - In-process key derivation for the `generic`, `github`, and `linear` policies, tested against vectors from `worklease key`.
  - Commands run from the invoking working directory, never the control root. The session comes from `--session`, `BL_SESSION`, or `WORKLEASE_SESSION_ID`, and otherwise is unscoped. MCP takes `session` explicitly and never reads it from the server's environment.
  - One claim per session: `handle-in-use` maps to `claim-held` (exit 4) naming the held item, never to contention.
  - The overlay polls `worklease --json list` while the TUI is visible and after each claim operation, and schedules expiry rechecks locally.
- `bl claim` and `bl release`. Contention exits 7 and names the holder.
- The §9 status-transition rules in the engine, so `bl start`, `bl done`, `bl set status=`, the TUI, and MCP `update` behave identically. Entering an active category acquires first and writes nothing on contention. Entering a closed category releases the session's claim after the write. Both report the claim and status outcomes separately. No automatic release of a verified run-managed claim; CLI/TUI invocation environment and explicit per-call MCP run context must be bound to the actual session/resource, never inferred from the MCP server's environment. Multi-item transitions into an active category are refused.
- In the TUI: the Claim column, `C`, a claim-gated `S`, and the `is:claimed` and `claimed:` queries.
- MCP `claim` and `release`, and a required `session` on `update` calls that change status while coordination is on.
- `resource` in `bl show --json`, plus `{resource}` and `BL_RESOURCE` for BL-30 and BL-31.
- Agent launch and worktree setup are documented action examples, not built-in behavior. No hook, action, or event field names Worklease.

The coordinator also owns typed session inspection and exact pending-acquire resumption for BL-33. Handle inspection is offline evidence, not proof of live ownership. Recovery data stays in Worklease; bl passes only opaque adapter references and exposes no credentials. MCP ownership context is explicit and request-scoped; unresolved context refuses side effects.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [ ] #1 Key vectors for each policy match `worklease key`; a script regenerates them, and a `BL_PROVIDER_TESTS=1` test compares them against the installed Worklease
- [ ] #2 `bl claim` under contention exits 7 naming the holder, `bl release` succeeds, and both JSON shapes are in docs/cli.md
- [ ] #3 With a fake Worklease, a failed status change after a successful claim keeps the claim and reports both outcomes
- [ ] #4 The overlay shows `mine`, `agent@host`, and expired, flips at expiry under an injected clock without polling, and never calls Worklease per row
- [ ] #5 Engine, readiness, and `next` code depend only on the coordination seam; a fake coordinator drives the same tests with no Worklease present
- [ ] #6 With BL_PROVIDER_TESTS=1, a round trip against Worklease 1.9.3 acquires through `bl start`, lists, and releases through `bl done` on an isolated claim
- [ ] #7 Inside a session that already holds the item claim (a `worklease run --resource` worker), `bl start` skips the acquire and changes status
- [ ] #8 Through start/done, set status, TUI and MCP update: active contention writes nothing (exit 7); closed writes then releases only the matching session's caller-managed claim and reports both outcomes. Verified run-managed claims are not released. MCP obtains ownership from validated per-call context, never from the server's WORKLEASE_RUN_ID or inherited handle.
- [ ] #9 An acquire while the session holds another active claim fails with `claim-held` (exit 4) naming the held item, and an MCP status change without `session` is refused before anything is written
- [ ] #10 `bl show --json` reports `resource`, equal to the key `bl claim` acquires
- [ ] #11 Session inspection and exact replay are exercised through a fake coordinator and pinned Worklease CLI fixtures: held/pending/absent/unresolved states stay distinct; replay preserves request identity and options, and corrupt/stale/foreign state never authorizes a fresh acquire.
- [ ] #12 One long-lived MCP server interleaves a verified run-managed session and a caller-managed session while its environment contains misleading run/session/handle selectors. Done preserves the supervisor's claim but releases the caller-managed claim; missing or mismatched explicit ownership context writes nothing and releases nothing.
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [ ] #1 task ci passes on the final commit
- [ ] #2 Every checked acceptance criterion has an AC#N evidence line in Implementation Notes naming the command and its result
- [ ] #3 Substantial changes had an independent verifier pass that returned PASS for every acceptance criterion
- [ ] #4 The diff stays within the task's modified-file list, or Implementation Notes justify the deviation
- [ ] #5 No test was deleted, skipped, or weakened
- [ ] #6 User-visible behavior changes are reflected in README.md or docs/ in the same change
<!-- DOD:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
1. Implement a narrow typed coordinator in model/engine for acquire/release/list plus session inspection and exact pending-acquire resumption. Inspection distinguishes absent, held, pending and unresolved/error; returns stable resource/claim identity and an opaque adapter-owned recovery reference, not tokens or Worklease structs. Unknown/stale/corrupt/foreign handles are never treated as an empty session. No public protocol or bl-owned recovery journal.
2. Map these operations to Worklease v1.9.3 stable CLI evidence: docs/claim-model.md and docs/cli-reference.md describe handle inspect (offline, not proof of live ownership), status/verify and retained acquire replay; internal/cli/lease_commands.go and remote_lifecycle.go show exact replay semantics. Record exact argv/JSON fixtures before implementation. Use authority-backed verification for held ownership and let Worklease retain/replay original request identity, resources, authority, session and options. Never synthesize a fresh acquire from an uncertain request or read private handle files as a new public API.
3. Preserve invoking cwd, authority/account selection and explicit session. Define request-scoped ownership as caller-managed, verified run-managed, or unresolved. CLI/TUI may obtain their invocation's run context from WORKLEASE_RUN_ID and inherited handle; MCP must receive any run/handle context explicitly per call and validate its binding to the session/resource through supported coordinator evidence. The server's environment and previous calls are never ownership evidence. Missing/mismatched evidence fails closed before claim or status side effects; a bare run ID or session-name prefix is not proof.
4. Clarify and document the exact optional MCP run-context schema and §7/§9 ownership rules in the implementation commit before freezing schemas. Strip inherited session/handle/run selectors from MCP child-process environments while supplying the verified per-call selection explicitly; never use global os.Setenv. If pinned Worklease cannot prove an ownership/recovery state, report unavailable/unknown with a safe recovery hint rather than inventing certainty.
5. Wire claim/release and active/closed engine transitions on all surfaces. Active reuses an already-held claim, refuses multi-item activation and retains newly acquired ownership on write failure. Closed writes then releases only the matching caller-managed claim synchronously; verified run-managed claims stay with their supervisor. Release failures leave status written and both outcomes visible. Emit claim events through BL-28, not data-derived readiness events.
6. Expose show resource and placeholder identity from the same key policy. Verify key vectors, overlay expiry, session/handle scoping, retained claims, exact pending replay and transition parity using fake coordinator/CLI fixtures. In one long-lived MCP server test interleave run-managed and caller-managed sessions with misleading server environment: done must preserve only the former. Run task test, task race and the isolated real-provider start/list/done round trip; update CLI/MCP/instructions docs.
7. Finish with task ci; record one AC#N evidence line per criterion with the actual command/result. Keep unmet checks open, obtain the required independent verification for substantial changes, and deliver the implementation before making dependent work eligible. Do not push or publish without explicit authorization.
<!-- SECTION:PLAN:END -->
