package normalize

import (
	"math"
	"testing"
)

func TestStripAtMentions(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"@alice must hold mutex", " must hold mutex"},
		{"see @bob-smith and @carol_doe", "see  and "},
		{"no mention here", "no mention here"},
	}
	for _, tc := range cases {
		if got := stripAtMentions(tc.in); got != tc.want {
			t.Errorf("stripAtMentions(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestStripFileLineRefs(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"see event.go:142", "see "},
		{"event.go:144 is wrong", " is wrong"},
		{"two refs foo.go:1 bar.go:2 done", "two refs   done"},
		{"no refs here", "no refs here"},
	}
	for _, tc := range cases {
		if got := stripFileLineRefs(tc.in); got != tc.want {
			t.Errorf("stripFileLineRefs(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestStripLineRefs(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"look at L42 here", "look at  here"},
		{"range L100 to L200", "range  to "},
		{"do not strip HEL123", "do not strip HEL123"},
		{"no refs", "no refs"},
	}
	for _, tc := range cases {
		if got := stripLineRefs(tc.in); got != tc.want {
			t.Errorf("stripLineRefs(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestStripTrailingPunct(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"done.", "done"},
		{"why?!", "why"},
		{"ok; ", "ok; "}, // trailing space means no match
		{"no internal. punct", "no internal. punct"},
		{"many...", "many"},
	}
	for _, tc := range cases {
		if got := stripTrailingPunct(tc.in); got != tc.want {
			t.Errorf("stripTrailingPunct(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestLower(t *testing.T) {
	if got := lower("Must Hold Mutex"); got != "must hold mutex" {
		t.Errorf("lower: got %q", got)
	}
}

func TestCollapseWhitespace(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"a  b", "a b"},
		{"a\t b\nc", "a b c"},
		{"single", "single"},
	}
	for _, tc := range cases {
		if got := collapseWhitespace(tc.in); got != tc.want {
			t.Errorf("collapseWhitespace(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestScrubAndHashAtMentionEquivalence(t *testing.T) {
	// AC2: @alice must hold mutex and must hold mutex produce the same cluster_key.
	a := ScrubAndHash("@alice must hold mutex")
	b := ScrubAndHash("must hold mutex")
	if a != b {
		t.Fatalf("@mention equivalence broken: %q vs %q", a, b)
	}
}

func TestScrubAndHashFileLineRefEquivalence(t *testing.T) {
	// AC3: see event.go:142 and see event.go:144 produce the same cluster_key.
	a := ScrubAndHash("see event.go:142")
	b := ScrubAndHash("see event.go:144")
	if a != b {
		t.Fatalf("file:line equivalence broken: %q vs %q", a, b)
	}
}

func TestScrubAndHashTrailingPunctEquivalence(t *testing.T) {
	a := ScrubAndHash("callers must hold the mutex.")
	b := ScrubAndHash("callers must hold the mutex")
	if a != b {
		t.Fatalf("trailing punct equivalence broken")
	}
}

func TestScrubAndHashCaseEquivalence(t *testing.T) {
	a := ScrubAndHash("Callers Must Hold The Mutex")
	b := ScrubAndHash("callers must hold the mutex")
	if a != b {
		t.Fatalf("case equivalence broken")
	}
}

func TestScrubAndHashWhitespaceEquivalence(t *testing.T) {
	a := ScrubAndHash("callers  must\thold   the\n\nmutex")
	b := ScrubAndHash("callers must hold the mutex")
	if a != b {
		t.Fatalf("whitespace equivalence broken")
	}
}

func TestScrubAndHashKnownFalseNegative(t *testing.T) {
	// Word order is not merged. This is documented as acceptable pilot
	// behavior — the normalize package's job is to be under-clustering.
	a := ScrubAndHash("callers must hold the mutex")
	b := ScrubAndHash("the mutex must be held by callers")
	if a == b {
		t.Fatalf("word-order pair unexpectedly merged: %s == %s", a, b)
	}
}

func TestScrubAndHashDistinctBodiesDiffer(t *testing.T) {
	a := ScrubAndHash("must hold mutex")
	b := ScrubAndHash("must release mutex")
	if a == b {
		t.Fatalf("distinct bodies collided: %s", a)
	}
}

func TestTokenJaccard(t *testing.T) {
	cases := []struct {
		a, b string
		want float64
	}{
		{"", "", 0},
		{"a b c", "a b c", 1},
		{"a b c", "d e f", 0},
		{"a b c", "a b d", 2.0 / 4.0},
		{"the mutex must be held", "callers must hold the mutex", 3.0 / 7.0},
	}
	for _, tc := range cases {
		got := TokenJaccard(tc.a, tc.b)
		if math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("TokenJaccard(%q, %q) = %f, want %f", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestTokenJaccardSymmetric(t *testing.T) {
	a := "the quick brown fox"
	b := "the lazy brown dog"
	if TokenJaccard(a, b) != TokenJaccard(b, a) {
		t.Fatalf("TokenJaccard is not symmetric")
	}
}
