package db

import (
	"reflect"
	"testing"
)

func TestPRIDSet_RoundTrip(t *testing.T) {
	cases := []struct {
		name string
		in   []int
		want []int
	}{
		{"empty", nil, nil},
		{"single", []int{42}, []int{42}},
		{"sorted", []int{1, 2, 3}, []int{1, 2, 3}},
		{"unsorted", []int{3, 1, 2}, []int{1, 2, 3}},
		{"dedup", []int{5, 5, 5}, []int{5}},
		{"mixed", []int{10, 1, 5, 10, 1}, []int{1, 5, 10}},
		{"top10", []int{100, 99, 98, 97, 96, 95, 94, 93, 92, 91}, []int{91, 92, 93, 94, 95, 96, 97, 98, 99, 100}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			blob := EncodePRIDSet(tc.in)
			out, err := DecodePRIDSet(blob)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if !reflect.DeepEqual(out, tc.want) {
				t.Errorf("round trip: got %v, want %v", out, tc.want)
			}
		})
	}
}

func TestPRIDSet_CompactSize(t *testing.T) {
	// Top-10 must fit in ~40 bytes.
	ids := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	blob := EncodePRIDSet(ids)
	if len(blob) != 40 {
		t.Errorf("top-10 encoded size = %d, want 40", len(blob))
	}
}

func TestPRIDSet_EmptyEncoding(t *testing.T) {
	if b := EncodePRIDSet(nil); b != nil {
		t.Errorf("EncodePRIDSet(nil) = %v, want nil", b)
	}
	if b := EncodePRIDSet([]int{}); b != nil {
		t.Errorf("EncodePRIDSet([]) = %v, want nil", b)
	}
}

func TestPRIDSet_DecodeInvalidLength(t *testing.T) {
	// Blob length not a multiple of 4 must error.
	if _, err := DecodePRIDSet([]byte{1, 2, 3}); err == nil {
		t.Error("decode 3-byte blob: expected error, got nil")
	}
}

func TestPRIDSet_NegativeFiltered(t *testing.T) {
	// Negative PR numbers are impossible; filter them silently.
	blob := EncodePRIDSet([]int{1, -5, 2, -1, 3})
	out, err := DecodePRIDSet(blob)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := []int{1, 2, 3}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("got %v, want %v", out, want)
	}
}

// --- ClaimsDB helpers ---

func TestSourceFilesPRIDSet_GetSetRoundTrip(t *testing.T) {
	db := tempDB(t)

	// Missing row returns sql.ErrNoRows semantics surfaced as err.
	_, _, err := db.GetPRIDSet("r", "pkg/a.go")
	if err == nil {
		t.Error("GetPRIDSet for missing row: expected err, got nil")
	}

	// Ensure + set.
	if err := db.SetPRIDSet("r", "pkg/a.go", []int{5, 3, 5, 9}, "gh-2.52.0"); err != nil {
		t.Fatalf("SetPRIDSet: %v", err)
	}

	ids, version, err := db.GetPRIDSet("r", "pkg/a.go")
	if err != nil {
		t.Fatalf("GetPRIDSet: %v", err)
	}
	want := []int{3, 5, 9}
	if !reflect.DeepEqual(ids, want) {
		t.Errorf("ids = %v, want %v", ids, want)
	}
	if version != "gh-2.52.0" {
		t.Errorf("version = %q, want %q", version, "gh-2.52.0")
	}
}

func TestSourceFilesPRIDSet_ClearPRIDSet(t *testing.T) {
	db := tempDB(t)

	if err := db.SetPRIDSet("r", "a.go", []int{1, 2}, "v1"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetPRIDSet("r", "b.go", []int{3, 4}, "v1"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetPRIDSet("other", "c.go", []int{7}, "v1"); err != nil {
		t.Fatal(err)
	}

	if err := db.ClearPRIDSet("r"); err != nil {
		t.Fatalf("ClearPRIDSet: %v", err)
	}

	// Repo 'r' cleared.
	ids, version, err := db.GetPRIDSet("r", "a.go")
	if err != nil {
		t.Fatalf("GetPRIDSet a.go: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("a.go ids after clear: %v, want empty", ids)
	}
	if version != "" {
		t.Errorf("a.go version after clear: %q, want empty", version)
	}

	// Other repo untouched.
	ids, version, err = db.GetPRIDSet("other", "c.go")
	if err != nil {
		t.Fatalf("GetPRIDSet c.go: %v", err)
	}
	if !reflect.DeepEqual(ids, []int{7}) {
		t.Errorf("other/c.go ids = %v, want [7]", ids)
	}
	if version != "v1" {
		t.Errorf("other/c.go version = %q, want v1", version)
	}
}

func TestSourceFilesPRIDSet_MarkNeedsRemine(t *testing.T) {
	db := tempDB(t)

	if err := db.SetPRIDSet("r", "a.go", []int{1, 2, 3}, "v1"); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkNeedsRemine("r", "a.go"); err != nil {
		t.Fatalf("MarkNeedsRemine: %v", err)
	}
	ids, version, err := db.GetPRIDSet("r", "a.go")
	if err != nil {
		t.Fatalf("GetPRIDSet: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("ids after needs_remine: %v, want empty", ids)
	}
	if version != PRMinerVersionNeedsRemine {
		t.Errorf("version = %q, want %q", version, PRMinerVersionNeedsRemine)
	}
}

func TestSourceFilesPRIDSet_AutoCreatesRow(t *testing.T) {
	db := tempDB(t)
	// MarkNeedsRemine on a file with no existing row should still work (creates placeholder).
	if err := db.MarkNeedsRemine("r", "new.go"); err != nil {
		t.Fatalf("MarkNeedsRemine on missing row: %v", err)
	}
	_, version, err := db.GetPRIDSet("r", "new.go")
	if err != nil {
		t.Fatalf("GetPRIDSet: %v", err)
	}
	if version != PRMinerVersionNeedsRemine {
		t.Errorf("version = %q, want %q", version, PRMinerVersionNeedsRemine)
	}
}
