package engine

import (
	"context"
	"slices"
	"strings"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/source"
)

// NextOutcome separates selection/ownership from the optional status write.
// Attempts records candidate claim decisions, in order; held/run resumes need none.
type NextOutcome struct {
	Attempts []NextAttempt      `json:"attempts"`
	Resumed  bool               `json:"resumed"`
	Claim    *model.ClaimResult `json:"claim,omitempty"`
	Status   string             `json:"status"`
}
type NextAttempt struct {
	Ref   model.Ref         `json:"ref"`
	Claim model.ClaimResult `json:"claim"`
}
type NextExplanation struct {
	Explanation
	Included bool `json:"included"`
}
type NextClaimResult struct {
	Items        []model.Item      `json:"items"`
	Explanations []NextExplanation `json:"explanations,omitempty"`
	NextOutcome
}
type NextClaimOptions struct {
	Query   string
	Start   bool
	Explain bool
}

// NextClaim inspects ownership before selecting work. Only an authoritative
// empty session enters one ranked snapshot, with one no-wait acquire per item.
// The coordinator, not a cached overlay, decides contention and ownership.
func NextClaim(ctx context.Context, cfg project.Config, options NextClaimOptions) (out NextClaimResult, err error) {
	out.Items = []model.Item{}
	out.Attempts = []NextAttempt{}
	out.Status = "not-requested"
	if options.Start {
		out.Status = "not-written"
	}
	c, caller := Coordination(ctx)
	if err = RequireSession(caller); err != nil {
		return out, err
	}
	if c == nil || cfg.Coordination.Worklease == nil {
		return out, source.Refuse("unavailable", "coordination is not configured")
	}
	var item model.Item
	var claim model.ClaimResult
	if caller.Run != "" {
		claim = c.VerifyRun(ctx, caller, "")
		out.Claim = &claim
		if claim.Outcome != model.ClaimRunManaged {
			return out, claimError(claim)
		}
		item, err = claimedNextItem(ctx, cfg, c, claim.Claim)
		if err != nil {
			return out, err
		}
		out.Resumed = true
	} else {
		state := c.InspectSession(ctx, caller)
		switch state.State {
		case model.SessionHeld, model.SessionPending:
			item, err = claimedNextItem(ctx, cfg, c, state.Claim)
			if err != nil {
				return out, err
			}
			if closed(item) {
				return out, source.Refuse("claim-held", "session holds closed item "+item.Ref.Source+":"+item.Ref.ID)
			}
			if state.State == model.SessionPending {
				claim = c.ResumeAcquire(ctx, caller, state.Recovery)
				claim = nextAcquireResult(claim)
				out.Attempts = append(out.Attempts, NextAttempt{item.Ref, claim})
				out.Claim = &claim
				if !ClaimSucceeded(claim) {
					return out, claimError(claim)
				}
				if claim.Claim == nil || state.Claim.ID != claim.Claim.ID || !slices.Equal(state.Claim.Resources, claim.Claim.Resources) {
					claim.Outcome, claim.Reason, claim.Message = model.ClaimUncertain, "unknown-outcome", "replay did not confirm the retained claim identity"
					out.Attempts[len(out.Attempts)-1].Claim = claim
					return out, claimError(claim)
				}
				recordClaimEvent(ctx, item, "claim.acquired", &claim)
				out.Attempts[len(out.Attempts)-1].Claim = claim
			} else {
				resource, _ := c.Resource(item)
				claim = model.ClaimResult{Outcome: model.ClaimHeld, Resource: resource, Claim: state.Claim, ExpiresAt: state.Claim.ExpiresAt}
				out.Claim = &claim
			}
			out.Resumed = true
		case model.SessionAbsent:
			// Ignore the claim overlay for selection: known contention must be
			// observable as an attempt, and List cannot authorize an acquire.
			selection := cfg
			selection.Coordination.Worklease = nil
			snap, e := LoadReads(ctx, selection, false)
			if e != nil {
				return out, e
			}
			q, e := snap.Parse(options.Query)
			if e != nil {
				return out, source.Refuse("usage", e.Error())
			}
			if len(q.Sort) > 0 || q.Group != "" {
				return out, source.Refuse("usage", "next uses readiness ranking; sort and group are not supported")
			}
			byRef := make(map[model.Ref]model.Item, len(snap.Query.Items))
			for _, candidate := range snap.Query.Items {
				byRef[candidate.Ref] = candidate
			}
			if options.Explain {
				defer func() {
					attempts := map[model.Ref]model.ClaimResult{}
					for _, attempt := range out.Attempts {
						attempts[attempt.Ref] = attempt.Claim
					}
					for _, explanation := range snap.Ranking.Items {
						candidate := byRef[explanation.Ref]
						included := len(out.Items) == 1 && out.Items[0].Ref == explanation.Ref
						if explanation.Readiness == Ready && !included {
							code := "selection-stopped"
							if !q.Match(candidate, snap.Query, nil) {
								code = "query"
							} else if attempt, ok := attempts[explanation.Ref]; ok {
								code = string(attempt.Outcome)
								if attempt.Reason != "" {
									code = attempt.Reason
								}
							}
							explanation.Reasons = append(slices.Clone(explanation.Reasons), Reason{Code: code})
						}
						out.Explanations = append(out.Explanations, NextExplanation{explanation, included})
					}
				}()
			}
			for _, ref := range snap.Ranking.Candidates {
				candidate := byRef[ref]
				if !q.Match(candidate, snap.Query, nil) {
					continue
				}
				// Reinspect for each attempt so a same-session inspection race
				// cannot authorize walking past newly held/pending ownership.
				var resumed bool
				claim, resumed = claimItem(ctx, candidate)
				claim = nextAcquireResult(claim)
				out.Attempts = append(out.Attempts, NextAttempt{ref, claim})
				out.Claim = &claim
				if claim.Outcome == model.ClaimContended {
					continue
				}
				if !ClaimSucceeded(claim) {
					return out, claimError(claim)
				}
				out.Resumed = resumed
				item = candidate
				break
			}
			if item.Ref.ID == "" {
				// Contention is an attempted candidate outcome, not failure to
				// execute next. The caller can see an exhausted snapshot.
				out.Claim = nil
				return out, nil
			}
		default:
			return out, source.Refuse("unavailable", "session ownership unresolved; "+state.Message)
		}
	}
	out.Items = append(out.Items, item)
	if !options.Start {
		return out, nil
	}
	status := cfg.Workflow.Start
	_, category, _ := StatusTarget(cfg, model.Mutation{Op: model.Set, Field: "status", Value: status})
	if status == "" || category != model.Active {
		return out, source.Refuse("rejected", "workflow.start must name an active status")
	}
	transition := TransitionResult{}
	updated, err := ApplyWrite(WithTransitionResult(ctx, &transition), cfg, model.Mutation{Ref: item.Ref, Op: model.Set, Field: "status", Value: status, ExpectedVersion: item.Version})
	if transition.Status != "" {
		out.Status = transition.Status
	}
	if err != nil {
		if out.Status != "written" {
			out.Status = "failed"
		}
		return out, err
	}
	out.Items[0] = updated
	return out, nil
}

func nextAcquireResult(claim model.ClaimResult) model.ClaimResult {
	if claim.Outcome == model.ClaimUncertain {
		claim.Reason = "unknown-outcome"
		if claim.Message == "" {
			claim.Message = "acquire outcome unknown; retain this session for recovery"
		}
	}
	return claim
}

// Prefer a source-qualified public work key. Otherwise match the one verified
// resource against source data, never treating missing/ambiguous state as free.
// This is identity resolution, not a readiness ranking or acquisition walk.
func claimedNextItem(ctx context.Context, cfg project.Config, c model.Coordinator, claim *model.ClaimInfo) (model.Item, error) {
	if claim == nil || claim.ID == "" || len(claim.Resources) != 1 {
		return model.Item{}, source.Refuse("unavailable", "claim item identity is unresolved")
	}
	var items []model.Item
	// Worklease supervisors may use a human label such as "run ID" as the
	// work key. Only a source-qualified bl ref is an item locator; the
	// verified resource remains authoritative for all other labels.
	sourceID, id, qualified := strings.Cut(claim.Item, ":")
	if qualified && id != "" && slices.ContainsFunc(cfg.Sources, func(s project.SourceConfig) bool { return s.ID == sourceID }) {
		var err error
		items, err = ReadDetail(ctx, cfg, claim.Item)
		if err != nil {
			return model.Item{}, err
		}
	} else {
		for _, configured := range cfg.Sources {
			backend, err := readSource(cfg, configured)
			if err != nil {
				return model.Item{}, err
			}
			delta, err := backend.Load(ctx, source.Cursor{IncludeArchive: true})
			if err != nil {
				return model.Item{}, err
			}
			if delta.Coverage != source.Complete {
				return model.Item{}, source.Refuse("unavailable", "cannot resolve claim from incomplete source")
			}
			items = append(items, delta.Items...)
		}
	}
	matches := []model.Item{}
	for _, item := range items {
		resource, err := c.Resource(item)
		if err != nil {
			return model.Item{}, source.Refuse("unavailable", err.Error())
		}
		if resource == claim.Resources[0] {
			matches = append(matches, item)
		}
	}
	if len(matches) != 1 {
		return model.Item{}, source.Refuse("unavailable", "claim item is missing or ambiguous; retain the session for recovery ("+strings.Join(claim.Resources, ", ")+")")
	}
	return matches[0], nil
}
