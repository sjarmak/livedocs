// Package mcpserver — errors_test.go pins the Error() string of the package's
// exported sentinels so the godoc claims (and the documented errors.Is
// discriminator usage in tribal_mine.go / mcp_mining_factory.go) cannot drift
// silently.
//
// Rationale (live_docs-m7v.39): the user-visible result text returned by the
// rate-limit denial wrapper is intentionally distinct from the sentinel's
// Error() string — text MAY be reworded for UX, but the sentinel string is
// the stable programmatic discriminator. A test that pins the sentinel string
// makes that contract enforceable rather than aspirational.
package mcpserver

import "testing"

func TestExportedSentinels_ErrorString(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "ErrRateLimited",
			err:  ErrRateLimited,
			want: "mcpserver: rate limit exceeded",
		},
		{
			name: "ErrLLMClientUnavailable",
			err:  ErrLLMClientUnavailable,
			want: "llm client unavailable",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.err == nil {
				t.Fatalf("%s is nil — sentinel must be a non-nil error value", tc.name)
			}
			if got := tc.err.Error(); got != tc.want {
				t.Fatalf("%s.Error() = %q, want %q (godoc claim drifted)",
					tc.name, got, tc.want)
			}
		})
	}
}
