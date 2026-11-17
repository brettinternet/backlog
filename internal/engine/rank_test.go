package engine

import (
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/brettinternet/backlog/internal/model"
)

// A key outside its bounds silently reorders Next, and a key ending in 0 leaves
// no room before it. Bounds include keys bl didn't write: imported a%016x keys,
// hand-written ones, and bytes outside 0-9a-z.
func TestRankKeySortsStrictlyBetween(t *testing.T) {
	t.Parallel()
	check := func(lo, hi string) {
		t.Helper()
		key, err := RankKey(lo, hi)
		if err != nil {
			t.Fatalf("RankKey(%q, %q): %v", lo, hi, err)
		}
		if key == "" || strings.Trim(key, rankDigits) != "" || strings.HasSuffix(key, "0") || lo != "" && key <= lo || hi != "" && key >= hi {
			t.Fatalf("RankKey(%q, %q) = %q", lo, hi, key)
		}
	}
	for _, pair := range [][2]string{
		{"", ""}, {"", "01"}, {"", "1"}, {"z", ""}, {"zzzz", ""}, {"a", "b"}, {"a", "a1"}, {"a", "a01"}, {"a0", "a1"},
		{"a1", "a2"}, {"a", "a00001"}, {"a0000000000000001", "a0000000000000002"}, {"a3", ""}, {"", "a3"}, {"az", "b"}, {"a", "az"},
		{"9", "a"}, {"zz", "zz1"}, {"a/", "b"}, {"", "A"}, {"A", ""}, {"a", "aA"}, {"a_", "b"},
	} {
		check(pair[0], pair[1])
	}
	// The shortest key: one character between open ends and wide gaps.
	if key, _ := RankKey("", ""); len(key) != 1 {
		t.Fatalf("open key = %q", key)
	}
	if key, _ := RankKey("a1", "a3"); key != "a2" {
		t.Fatalf("RankKey(a1, a3) = %q", key)
	}
	// Nothing in 0-9a-z sorts between these; refusing beats a wrong key.
	for _, pair := range [][2]string{{"", "0"}, {"a", "a0"}, {"a", "a00"}, {"a", "a"}, {"b", "a"}, {"A", "a"}, {"a~", "b"}, {"a", "a/1"}} {
		if key, err := RankKey(pair[0], pair[1]); err == nil {
			t.Fatalf("RankKey(%q, %q) = %q, want a refusal", pair[0], pair[1], key)
		}
	}
	random := rand.New(rand.NewPCG(1, 2))
	word := func() string {
		b := make([]byte, random.IntN(5))
		for i := range b {
			b[i] = rankDigits[random.IntN(len(rankDigits))]
		}
		return string(b)
	}
	for range 20000 {
		lo, hi := word(), word()
		if lo != "" && hi != "" && lo > hi {
			lo, hi = hi, lo
		}
		if hi != "" && strings.HasPrefix(hi, lo) && strings.Trim(hi[len(lo):], "0") == "" {
			continue // nothing in 0-9a-z sorts between a key and itself followed by zeros
		}
		check(lo, hi)
	}
	// Repeated inserts at one spot keep finding room.
	lo, hi := "a", "b"
	for range 200 {
		key, err := RankKey(lo, hi)
		if err != nil {
			t.Fatal(err)
		}
		if random.IntN(2) == 0 {
			lo = key
		} else {
			hi = key
		}
	}
}

// A move beside an unranked item ranks only the unranked run before the gap,
// keeps every other item's relative order, and writes nothing else.
func TestPlanMove(t *testing.T) {
	t.Parallel()
	items := func(ranks ...string) []model.Item {
		out := make([]model.Item, len(ranks))
		for n, rank := range ranks {
			out[n] = model.Item{Ref: model.Ref{Source: "tasks", ID: string(rune('A' + n))}, Rank: rank, Version: "v"}
		}
		return out
	}
	ref := func(id string) model.Ref { return model.Ref{Source: "tasks", ID: id} }
	apply := func(order []model.Item, writes []RankWrite) ([]string, []string) {
		order = slices.Clone(order)
		var written []string
		for _, w := range writes {
			for n := range order {
				if order[n].Ref == w.Item.Ref && order[n].Rank != w.Rank {
					order[n].Rank = w.Rank
					written = append(written, w.Item.Ref.ID)
				}
			}
		}
		order = RankOrder(order, func() (next []model.Ref) {
			for _, i := range items("", "", "", "", "", "", "") {
				next = append(next, i.Ref)
			}
			return next
		}())
		var ids []string
		for _, i := range order {
			ids = append(ids, i.Ref.ID)
		}
		return ids, written
	}
	for _, tc := range []struct {
		name    string
		ranks   []string
		moving  []string
		target  string
		after   bool
		want    string
		written string
	}{
		{"between ranked", []string{"a", "b", "c", ""}, []string{"D"}, "B", false, "ADBC", "D"},
		{"after last ranked", []string{"a", "b", "", ""}, []string{"D"}, "B", true, "ABDC", "D"},
		{"top", []string{"a", "b", ""}, []string{"C"}, "A", false, "CAB", "C"},
		{"after unranked", []string{"a", "", "", "", "", ""}, []string{"F"}, "C", true, "ABCFDE", "BCF"},
		{"before unranked", []string{"a", "", "", "", "", ""}, []string{"F"}, "C", false, "ABFCDE", "BF"},
		{"before first unranked", []string{"a", "", "", ""}, []string{"D"}, "B", false, "ADBC", "D"},
		{"all unranked", []string{"", "", "", ""}, []string{"A"}, "C", true, "BCAD", "BCA"},
		{"several in order", []string{"a", "b", "c", "d"}, []string{"D", "A"}, "B", true, "BDAC", "DA"},
		{"up one row", []string{"a", "b", "c"}, []string{"C"}, "B", false, "ACB", "C"},
		{"already in place", []string{"a", "b", "c"}, []string{"B"}, "A", true, "ABC", ""},
		{"duplicate neighbor keys refuse", []string{"a", "a", "z"}, []string{"C"}, "A", true, "", ""},
	} {
		order := items(tc.ranks...)
		var moving []model.Ref
		for _, id := range tc.moving {
			moving = append(moving, ref(id))
		}
		writes, err := PlanMove(order, moving, ref(tc.target), tc.after)
		if tc.want == "" {
			if err == nil {
				t.Fatalf("%s: want a refusal, got %v", tc.name, writes)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		ids, written := apply(order, writes)
		if got := strings.Join(ids, ""); got != tc.want {
			t.Fatalf("%s: order %s, want %s", tc.name, got, tc.want)
		}
		if got := strings.Join(written, ""); got != tc.written {
			t.Fatalf("%s: wrote %s, want %s", tc.name, got, tc.written)
		}
	}
}
