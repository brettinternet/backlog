package engine

import (
	"context"
	"fmt"
	"math/big"
	"slices"
	"strings"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/query"
	"github.com/brettinternet/backlog/internal/source"
)

// rankDigits are the characters generated rank keys use, in byte order, so keys
// stay plain YAML scalars.
const rankDigits = "0123456789abcdefghijklmnopqrstuvwxyz"

// RankKey returns the shortest key that sorts bytewise strictly between lo and
// hi, where "" is an open end. Of the shortest keys it takes the middle one,
// and never one ending in 0, which would leave no key between it and its
// prefix. lo and hi may be any existing keys, including ones bl didn't write.
func RankKey(lo, hi string) (string, error) {
	if lo == "" || hi == "" || lo < hi {
		for length := 1; length <= max(len(lo), len(hi))+2; length++ {
			first, ok := rankAbove(lo, length)
			last, ok2 := rankBelow(hi, length)
			if !ok || !ok2 {
				continue
			}
			a, _ := new(big.Int).SetString(first, 36)
			b, _ := new(big.Int).SetString(last, 36)
			if a.Cmp(b) > 0 {
				continue
			}
			mid := new(big.Int).Rsh(new(big.Int).Add(a, b), 1)
			if new(big.Int).Mod(mid, big.NewInt(36)).Sign() == 0 {
				switch {
				case mid.Cmp(b) < 0:
					mid.Add(mid, big.NewInt(1))
				case mid.Cmp(a) > 0:
					mid.Sub(mid, big.NewInt(1))
				default:
					continue
				}
			}
			key := mid.Text(36)
			return strings.Repeat("0", length-len(key)) + key, nil
		}
	}
	return "", source.Refuse("rejected", fmt.Sprintf("no rank key fits between %q and %q; change one of them", lo, hi))
}

// rankAbove is the least key of length characters from rankDigits above lo.
func rankAbove(lo string, length int) (string, bool) {
	if lo == "" {
		return strings.Repeat("0", length), true
	}
	digits := rankPrefix(lo)
	if len(lo) < length && digits == len(lo) {
		return lo + strings.Repeat("0", length-len(lo)), true
	}
	for j := min(digits, length-1, len(lo)-1); j >= 0; j-- {
		if n := strings.IndexFunc(rankDigits, func(r rune) bool { return byte(r) > lo[j] }); n >= 0 {
			return lo[:j] + rankDigits[n:n+1] + strings.Repeat("0", length-j-1), true
		}
	}
	return "", false
}

// rankBelow is the greatest key of length characters from rankDigits below hi.
func rankBelow(hi string, length int) (string, bool) {
	if hi == "" {
		return strings.Repeat("z", length), true
	}
	digits := rankPrefix(hi)
	if length < len(hi) && digits >= length {
		return hi[:length], true
	}
	for j := min(digits, length-1, len(hi)-1); j >= 0; j-- {
		if n := strings.LastIndexFunc(rankDigits, func(r rune) bool { return byte(r) < hi[j] }); n >= 0 {
			return hi[:j] + rankDigits[n:n+1] + strings.Repeat("z", length-j-1), true
		}
	}
	return "", false
}

// rankPrefix counts the leading bytes of key that are rankDigits.
func rankPrefix(key string) int {
	for i := range len(key) {
		if !strings.ContainsRune(rankDigits, rune(key[i])) {
			return i
		}
	}
	return len(key)
}

// rankKeys spreads n keys strictly between lo and hi, in order, by bisection so
// each stays short.
func rankKeys(lo, hi string, n int) ([]string, error) {
	keys := make([]string, n)
	var fill func(lo, hi string, from, to int) error
	fill = func(lo, hi string, from, to int) error {
		if from >= to {
			return nil
		}
		mid := (from + to) / 2
		key, err := RankKey(lo, hi)
		if err != nil {
			return err
		}
		keys[mid] = key
		if err := fill(lo, key, from, mid); err != nil {
			return err
		}
		return fill(key, hi, mid+1, to)
	}
	return keys, fill(lo, hi, 0, n)
}

// RankWrite is one rank a move writes.
type RankWrite struct {
	Item model.Item
	Rank string
}

// RankOrder sorts one source's items as sort:rank shows them: ranked items by
// key, then unranked ones in next order, given as the engine ranking's refs.
func RankOrder(items []model.Item, next []model.Ref) []model.Item {
	items = slices.Clone(items)
	query.Snapshot{Next: next}.Sort(items, []query.SortKey{{Field: "rank"}})
	return items
}

// PlanMove places moving, in order, contiguously before or after target in
// order, one source's items in RankOrder. It writes only rank and never
// rewrites a neighbor. Placing beside an unranked item first ranks the unranked
// items from the first one through the item before the gap, after the
// greatest key, so nothing else changes position. The returned items are every
// one the move places, in order, with the ranks to write; an item already at
// its rank needs no write.
func PlanMove(order []model.Item, moving []model.Ref, target model.Ref, after bool) ([]RankWrite, error) {
	index := func(items []model.Item, ref model.Ref) int {
		return slices.IndexFunc(items, func(i model.Item) bool { return i.Ref == ref })
	}
	if len(moving) == 0 {
		return nil, source.Refuse("usage", "name an item to move")
	}
	moved := make([]model.Item, 0, len(moving))
	for n, ref := range moving {
		if ref == target {
			return nil, source.Refuse("rejected", "can't move "+ref.ID+" beside itself")
		}
		if slices.Contains(moving[:n], ref) {
			return nil, source.Refuse("usage", ref.ID+" is named twice")
		}
		i := index(order, ref)
		if i < 0 {
			return nil, source.Refuse("not-found", ref.Source+":"+ref.ID+" is not an active item of "+target.Source)
		}
		moved = append(moved, order[i])
	}
	rest := slices.DeleteFunc(slices.Clone(order), func(i model.Item) bool { return slices.Contains(moving, i.Ref) })
	gap := index(rest, target)
	if gap < 0 {
		return nil, source.Refuse("not-found", target.Source+":"+target.ID+" is not an active item")
	}
	if after {
		gap++
	}
	lo, hi, from := "", "", gap
	if gap < len(rest) {
		hi = rest[gap].Rank
	}
	if gap > 0 && rest[gap-1].Rank == "" {
		from = slices.IndexFunc(rest, func(i model.Item) bool { return i.Rank == "" })
	}
	if from > 0 {
		lo = rest[from-1].Rank
	}
	placed := append(slices.Clone(rest[from:gap]), moved...)
	// Moving items already in place, between their neighbors, writes nothing.
	keys := make([]string, len(placed))
	for n, item := range placed {
		keys[n] = item.Rank
	}
	inPlace := from == gap && slices.IsSorted(keys) && !slices.Contains(keys, "") && keys[0] > lo && (hi == "" || keys[len(keys)-1] < hi)
	for n := 1; inPlace && n < len(keys); n++ {
		inPlace = keys[n-1] != keys[n]
	}
	if !inPlace {
		var err error
		if keys, err = rankKeys(lo, hi, len(placed)); err != nil {
			return nil, err
		}
	}
	writes := make([]RankWrite, len(placed))
	for n, item := range placed {
		writes[n] = RankWrite{Item: item, Rank: keys[n]}
	}
	return writes, nil
}

// MoveSource reports why items of sourceID can't be reordered, or "". Only
// native sources store bl's rank keys (D10).
func MoveSource(cfg project.Config, sourceID string) string {
	p, local := cfg.SourceProject(sourceID)
	for _, c := range p.Sources {
		if c.ID == local {
			if c.Type == "native" {
				return ""
			}
			return "source " + sourceID + " (" + c.Type + ") can't be reordered; only native sources store rank"
		}
	}
	return "unknown source " + sourceID
}

// MoveMutations are the rank writes of a move, each bound to the version it
// was planned from.
func MoveMutations(writes []RankWrite) []model.Mutation {
	var out []model.Mutation
	for _, w := range writes {
		if w.Item.Rank != w.Rank {
			out = append(out, model.Mutation{Ref: w.Item.Ref, ExpectedVersion: w.Item.Version, Op: model.Set, Field: "rank", Value: w.Rank})
		}
	}
	return out
}

// Move places moving, in order, contiguously before or after target, writing
// only rank. Each moving item's Version is the version its write expects. It
// returns every placed item, in order, as written or as it was when already in
// place; after a failed write, those written before it.
func Move(ctx context.Context, cfg project.Config, moving []model.Item, target model.Item, after bool) ([]model.Item, error) {
	sourceID := target.Ref.Source
	if reason := MoveSource(cfg, sourceID); reason != "" {
		return nil, source.Refuse("rejected", reason)
	}
	refs := make([]model.Ref, len(moving))
	for n, item := range moving {
		if item.Ref.Source != sourceID {
			return nil, source.Refuse("rejected", "moves stay within one source; "+item.Ref.ID+" is in "+item.Ref.Source+", "+target.Ref.ID+" in "+sourceID)
		}
		refs[n] = item.Ref
	}
	reads, err := LoadReads(ctx, cfg, false)
	if err != nil {
		return nil, err
	}
	items := slices.DeleteFunc(slices.Clone(reads.Query.Items), func(i model.Item) bool { return i.Ref.Source != sourceID })
	writes, err := PlanMove(RankOrder(items, reads.Query.Next), refs, target.Ref, after)
	if err != nil {
		return nil, err
	}
	out := make([]model.Item, 0, len(writes))
	for _, w := range writes {
		if n := slices.IndexFunc(moving, func(i model.Item) bool { return i.Ref == w.Item.Ref }); n >= 0 {
			w.Item.Version = moving[n].Version
		}
		if w.Item.Rank == w.Rank {
			out = append(out, w.Item)
			continue
		}
		updated, err := ApplyWrite(ctx, cfg, MoveMutations([]RankWrite{w})[0])
		if err != nil {
			return out, err
		}
		out = append(out, updated)
	}
	return out, nil
}
