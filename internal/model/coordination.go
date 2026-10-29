package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// CallerContext is request-scoped. A run is evidence to verify, not permission.
type CallerContext struct {
	Session string `json:"session"`
	Run     string `json:"run,omitempty"`
	// PerItem coordinates each item under its own session (design.md D39).
	PerItem bool `json:"-"`
	// Agent is the detected agent kind, and AgentSession its harness's own
	// session ID. Events record them; coordination ignores them (D40).
	Agent        string `json:"-"`
	AgentSession string `json:"-"`
}

// For returns the caller for one claim resource: SESSION:HASH when per item,
// so one person can hold many claims. Every source and key of the item finds
// the same claim, and the name fits Worklease's 128-byte session limit.
func (c CallerContext) For(resource string) CallerContext {
	if c.PerItem {
		sum := sha256.Sum256([]byte(resource))
		c.Session += ":" + hex.EncodeToString(sum[:16])
		c.PerItem = false
	}
	return c
}

type ClaimInfo struct {
	ID        string    `json:"claimId"`
	Resources []string  `json:"resources"`
	Session   string    `json:"session"`
	Agent     string    `json:"agent"`
	Item      string    `json:"item,omitempty"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// ClaimEpoch is one retained claim on an item's resource. An open epoch has
// no end; ExpiresAt is set when its current lease is known.
type ClaimEpoch struct {
	ID         string    `json:"claimId"`
	Session    string    `json:"session"`
	Agent      string    `json:"agent"`
	AcquiredAt time.Time `json:"acquiredAt"`
	EndedAt    time.Time `json:"endedAt,omitzero"`
	EndReason  string    `json:"endReason,omitempty"`
	ExpiresAt  time.Time `json:"expiresAt,omitzero"`
}

// ItemSession is one caller session that worked an item, folded from its
// events and claim history (design.md D40). A claim-only session, such as one
// from another host, has only its ID and Claim.
type ItemSession struct {
	ID           string      `json:"id"`
	Agent        string      `json:"agent,omitempty"`
	AgentSession string      `json:"agentSession,omitempty"`
	Host         string      `json:"host,omitempty"`
	CWD          string      `json:"cwd,omitempty"`
	Branch       string      `json:"branch,omitempty"`
	FirstSeen    time.Time   `json:"firstSeen"`
	LastSeen     time.Time   `json:"lastSeen"`
	Phases       []string    `json:"phases,omitempty"`
	Claim        *ClaimEpoch `json:"claim,omitempty"`
	// Ended is done, released, or lapsed; empty while the session is open.
	Ended string `json:"ended,omitempty"`
}
type ClaimOutcome string

const (
	ClaimAcquired    ClaimOutcome = "acquired"
	ClaimContended   ClaimOutcome = "contended"
	ClaimUncertain   ClaimOutcome = "uncertain"
	ClaimUnavailable ClaimOutcome = "unavailable"
	ClaimHeld        ClaimOutcome = "held"
	ClaimReleased    ClaimOutcome = "released"
	ClaimNotHeld     ClaimOutcome = "not-held"
	ClaimRenewed     ClaimOutcome = "renewed"
	ClaimRunManaged  ClaimOutcome = "run-managed"
)

type ClaimResult struct {
	Outcome    ClaimOutcome `json:"outcome"`
	EventError string       `json:"eventError,omitempty"`
	Resource   string       `json:"resource,omitempty"`
	Claim      *ClaimInfo   `json:"claim,omitempty"`
	Reason     string       `json:"reason,omitempty"`
	Message    string       `json:"message,omitempty"`
	ExpiresAt  time.Time    `json:"expiresAt,omitzero"`
}

// RecoveryRef is opaque adapter-owned evidence, never a credential or request.
type RecoveryRef interface{ CoordinationRecovery() }
type SessionKind string

const (
	SessionAbsent     SessionKind = "absent"
	SessionHeld       SessionKind = "held"
	SessionPending    SessionKind = "pending"
	SessionUnresolved SessionKind = "unresolved"
)

type SessionState struct {
	State    SessionKind // absent, held, pending, unresolved
	Claim    *ClaimInfo
	Recovery RecoveryRef
	Message  string
}

// Coordinator is internal: no provider types or credentials cross this seam.
type Coordinator interface {
	Resource(Item) (string, error)
	InspectSession(context.Context, CallerContext) SessionState
	ResumeAcquire(context.Context, CallerContext, RecoveryRef) ClaimResult
	Acquire(context.Context, CallerContext, Item) ClaimResult
	// Release must guard the mutation with the previously verified claim ID.
	Release(context.Context, CallerContext, Item, string) ClaimResult
	Heartbeat(context.Context, CallerContext) ClaimResult
	List(context.Context) ([]ClaimInfo, error)
	// History lists retained claim epochs for one resource, oldest first.
	History(context.Context, string) ([]ClaimEpoch, error)
	VerifyRun(context.Context, CallerContext, string) ClaimResult
}

// Label evaluates expiry locally with an injected time, without provider I/O.
func (c ClaimInfo) Label(session string, now time.Time) string {
	if !c.ExpiresAt.After(now) {
		return "expired"
	}
	if session != "" && c.Session == session {
		return "mine"
	}
	return c.Agent
}
