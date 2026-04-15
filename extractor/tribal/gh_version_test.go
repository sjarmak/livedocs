package tribal

import (
	"context"
	"errors"
	"testing"
)

// fakeGhRunner returns the given canned `gh --version` output.
func fakeGhRunner(output string, err error) CommandRunner {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		if err != nil {
			return nil, err
		}
		return []byte(output), nil
	}
}

func TestGhVersionPreflight_KnownVersionAccepted(t *testing.T) {
	runner := fakeGhRunner("gh version 2.52.0 (2024-06-25)\nhttps://github.com/cli/cli/releases/tag/v2.52.0\n", nil)

	v, err := CheckGhVersion(context.Background(), runner, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != "2.52.0" {
		t.Errorf("version = %q, want 2.52.0", v)
	}
}

func TestGhVersionPreflight_UnknownVersionRejected(t *testing.T) {
	runner := fakeGhRunner("gh version 1.0.0 (2020-01-01)\n", nil)

	_, err := CheckGhVersion(context.Background(), runner, false)
	if err == nil {
		t.Fatal("expected ErrUnknownGhVersion, got nil")
	}
	if !errors.Is(err, ErrUnknownGhVersion) {
		t.Errorf("expected ErrUnknownGhVersion, got %v", err)
	}
}

func TestGhVersionPreflight_UnknownVersionWithAcceptFlag(t *testing.T) {
	runner := fakeGhRunner("gh version 3.99.0 (2099-12-31)\n", nil)

	v, err := CheckGhVersion(context.Background(), runner, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != "3.99.0" {
		t.Errorf("version = %q, want 3.99.0", v)
	}
}

func TestGhVersionPreflight_ParseFailureRejected(t *testing.T) {
	runner := fakeGhRunner("not a gh version line\n", nil)

	_, err := CheckGhVersion(context.Background(), runner, false)
	if err == nil {
		t.Fatal("expected error on unparseable output, got nil")
	}
	if !errors.Is(err, ErrUnknownGhVersion) {
		t.Errorf("expected ErrUnknownGhVersion, got %v", err)
	}
}

func TestGhVersionPreflight_ParseFailureWithAcceptFlag(t *testing.T) {
	runner := fakeGhRunner("garbage output\n", nil)

	v, err := CheckGhVersion(context.Background(), runner, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != "unknown" {
		t.Errorf("version = %q, want unknown", v)
	}
}

func TestGhVersionPreflight_RunnerError(t *testing.T) {
	runner := fakeGhRunner("", errors.New("gh: command not found"))

	_, err := CheckGhVersion(context.Background(), runner, true)
	if err == nil {
		t.Fatal("expected error from failing runner, got nil")
	}
}

func TestParseGhVersion(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"gh version 2.52.0 (2024-06-25)\n", "2.52.0"},
		{"gh version 2.60.0\n", "2.60.0"},
		{"\ngh version 2.50.0\nextra\n", "2.50.0"},
		{"not gh\n", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := parseGhVersion(tc.in); got != tc.want {
			t.Errorf("parseGhVersion(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
