// Package tribal — limiter_test.go covers KeyedLimiter, a bounded-keyspace
// token-bucket rate limiter designed for per-session throttling of the
// tribal_mine_on_demand MCP tool (live_docs-m7v.22) and as a reusable
// primitive for bounding the singleflight key-space in
// TribalMiningService.MineFile (live_docs-m7v.17).
package tribal

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// Burst determinism: we rely on a large Burst() value with a small Rate so
// the first N calls succeed without blocking on time passage, and the
// (N+1)-th call is denied. Tests that exercise recovery use short sleeps
// bounded by a small rate (10 tokens/sec => 100ms per token).

func TestKeyedLimiter_SingleKeyBurstThenDenied(t *testing.T) {
	l := NewKeyedLimiter(KeyedLimiterConfig{
		Rate:        1, // 1 token/sec — negligible refill during test
		Burst:       3,
		MaxKeys:     16,
		AnonymousID: "anon",
	})
	defer l.Close()

	for i := 0; i < 3; i++ {
		if !l.Allow("sess-A") {
			t.Fatalf("call %d: want Allow=true within burst, got false", i)
		}
	}
	// 4th call in quick succession must be denied.
	if l.Allow("sess-A") {
		t.Error("4th call should be denied after burst exhausted")
	}
}

func TestKeyedLimiter_SeparateSessionsIsolated(t *testing.T) {
	l := NewKeyedLimiter(KeyedLimiterConfig{
		Rate:        1,
		Burst:       2,
		MaxKeys:     16,
		AnonymousID: "anon",
	})
	defer l.Close()

	// Session A exhausts its burst.
	for i := 0; i < 2; i++ {
		if !l.Allow("sess-A") {
			t.Fatalf("sess-A call %d denied prematurely", i)
		}
	}
	if l.Allow("sess-A") {
		t.Error("sess-A 3rd call should be denied")
	}

	// Session B must still have its full burst regardless of A's state.
	for i := 0; i < 2; i++ {
		if !l.Allow("sess-B") {
			t.Fatalf("sess-B call %d denied — leaked bucket from sess-A", i)
		}
	}
}

func TestKeyedLimiter_EmptyKeyUsesAnonymous(t *testing.T) {
	l := NewKeyedLimiter(KeyedLimiterConfig{
		Rate:        1,
		Burst:       2,
		MaxKeys:     16,
		AnonymousID: "anon-bucket",
	})
	defer l.Close()

	// Empty key -> anon bucket, shared across all empty-key callers.
	if !l.Allow("") {
		t.Error("empty-key call 1 should succeed")
	}
	if !l.Allow("") {
		t.Error("empty-key call 2 should succeed")
	}
	if l.Allow("") {
		t.Error("empty-key call 3 should be denied (anon bucket exhausted)")
	}
}

// Security regression: a client sending the configured AnonymousID as its
// own session ID must NOT share the anonymous bucket. Internally the
// anonymous bucket is NUL-prefixed so no identifier-style session ID can
// collide with it. Without this separation, any HTTP/SSE client could drain
// the stdio-anonymous bucket by setting session_id="anonymous".
func TestKeyedLimiter_ExplicitAnonymousIDDoesNotCollide(t *testing.T) {
	l := NewKeyedLimiter(KeyedLimiterConfig{
		Rate:        1,
		Burst:       2,
		MaxKeys:     16,
		AnonymousID: "anon-bucket",
	})
	defer l.Close()

	// Exhaust the true anonymous bucket via empty key.
	if !l.Allow("") || !l.Allow("") {
		t.Fatal("anon bucket should grant its full burst")
	}
	if l.Allow("") {
		t.Fatal("anon bucket should be exhausted after burst")
	}
	// A client that explicitly sends "anon-bucket" as its session ID must
	// get a SEPARATE bucket, not the exhausted anonymous one.
	if !l.Allow("anon-bucket") {
		t.Error("explicit 'anon-bucket' session ID must not collide with the anonymous bucket")
	}
	if !l.Allow("anon-bucket") {
		t.Error("explicit 'anon-bucket' session ID must have its own independent burst")
	}
}

func TestKeyedLimiter_LRUEvictionBoundsMap(t *testing.T) {
	const maxKeys = 4
	l := NewKeyedLimiter(KeyedLimiterConfig{
		Rate:        1,
		Burst:       1,
		MaxKeys:     maxKeys,
		AnonymousID: "anon",
	})
	defer l.Close()

	// Seed maxKeys distinct sessions — each exhausts its bucket on one call.
	keys := []string{"sess-1", "sess-2", "sess-3", "sess-4"}
	for _, k := range keys {
		if !l.Allow(k) {
			t.Fatalf("%s: first call should succeed", k)
		}
	}

	// All seeded keys are at zero tokens. Verify denial.
	for _, k := range keys {
		if l.Allow(k) {
			t.Errorf("%s: should be denied after single-burst exhaustion", k)
		}
	}
	if got := l.Size(); got != maxKeys {
		t.Fatalf("Size() = %d, want %d", got, maxKeys)
	}

	// Insert a new session — this MUST evict the oldest (sess-1) from the
	// active map. The active map stays bounded at maxKeys.
	if !l.Allow("sess-5") {
		t.Fatal("sess-5 first call should succeed")
	}
	if got := l.Size(); got != maxKeys {
		t.Fatalf("after eviction, Size() = %d, want %d", got, maxKeys)
	}

	// sess-1 was evicted from the active map but its bucket state is
	// preserved in the snapshot cache (live_docs-m7v.24 — thrash-reset
	// defense). On re-insertion sess-1 retains its exhausted state, so the
	// call is denied — no free burst from eviction cycling.
	if l.Allow("sess-1") {
		t.Error("sess-1 should remain denied after LRU eviction — bucket state must persist across eviction")
	}
}

func TestKeyedLimiter_LRUTouchOnAllow(t *testing.T) {
	const maxKeys = 3
	l := NewKeyedLimiter(KeyedLimiterConfig{
		Rate:        1,
		Burst:       5, // large burst so repeated calls don't exhaust
		MaxKeys:     maxKeys,
		AnonymousID: "anon",
	})
	defer l.Close()

	// Seed 3 keys.
	for _, k := range []string{"a", "b", "c"} {
		_ = l.Allow(k)
	}
	// Touch 'a' to make it most-recently-used.
	_ = l.Allow("a")
	// Insert a 4th key — evicts LRU, which must now be 'b' (not 'a').
	_ = l.Allow("d")

	// 'a' should still be in the map (not evicted).
	// We verify via Size() + presence check.
	if !l.hasKey("a") {
		t.Error("'a' was evicted despite recent Allow — LRU not touching on access")
	}
	if l.hasKey("b") {
		t.Error("'b' should have been evicted as the LRU entry")
	}
}

func TestKeyedLimiter_Refill(t *testing.T) {
	// 10 tokens/sec with burst=1 => refill period = 100ms. Wait 110ms and
	// verify a second call succeeds.
	l := NewKeyedLimiter(KeyedLimiterConfig{
		Rate:        10,
		Burst:       1,
		MaxKeys:     4,
		AnonymousID: "anon",
	})
	defer l.Close()

	if !l.Allow("k") {
		t.Fatal("first call should succeed")
	}
	if l.Allow("k") {
		t.Fatal("second call should be denied (burst=1)")
	}
	time.Sleep(150 * time.Millisecond)
	if !l.Allow("k") {
		t.Error("after refill wait, call should succeed")
	}
}

func TestKeyedLimiter_ConcurrentAccessRace(t *testing.T) {
	l := NewKeyedLimiter(KeyedLimiterConfig{
		Rate:        100,
		Burst:       1000,
		MaxKeys:     32,
		AnonymousID: "anon",
	})
	defer l.Close()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			key := fmt.Sprintf("sess-%d", id%4) // 4 distinct keys across 8 goroutines
			for j := 0; j < 50; j++ {
				_ = l.Allow(key)
			}
		}(i)
	}
	wg.Wait()
	// The only assertion under -race is that we complete without data race.
	// Size must stay bounded.
	if got := l.Size(); got > 4 {
		t.Errorf("Size() = %d, want <= 4 (bounded keyspace)", got)
	}
}

// TestKeyedLimiter_ThrashDoesNotRefreshBurstState is the regression test for
// live_docs-m7v.24. Before the fix, LRU eviction discarded an evicted key's
// *rate.Limiter; when the same key returned, a fresh limiter was created with
// a full burst. That let an attacker with MaxKeys+1 distinct IDs continuously
// cycle a victim through evict/re-insert, resetting the victim's partial-drain
// state on every cycle — effectively nullifying the rate limit for the victim.
//
// Fix contract: the bucket state (tokens, last refill time) for an evicted key
// must be restored when the same key returns, up to a bounded snapshot cache.
// So a victim that has drained their burst before being evicted MUST still be
// denied immediately after returning, assuming elapsed wall-clock time is
// shorter than the refill interval.
func TestKeyedLimiter_ThrashDoesNotRefreshBurstState(t *testing.T) {
	const maxKeys = 2
	// Rate = 0.001 tok/sec => one token every ~1000s; the test's wall-clock
	// window (milliseconds) refills effectively zero tokens, so any "fresh
	// burst" after re-insertion can only come from a new rate.Limiter
	// (i.e., the bug).
	l := NewKeyedLimiter(KeyedLimiterConfig{
		Rate:        0.001,
		Burst:       3,
		MaxKeys:     maxKeys,
		AnonymousID: "anon",
	})
	defer l.Close()

	// Victim exhausts their burst.
	for i := 0; i < 3; i++ {
		if !l.Allow("victim") {
			t.Fatalf("victim call %d: want Allow=true within burst, got false", i)
		}
	}
	if l.Allow("victim") {
		t.Fatal("victim should be denied after exhausting burst")
	}

	// Attacker thrashes with MaxKeys+1 distinct synthetic IDs repeatedly.
	// This cycles the map and forces victim eviction + later re-insertion
	// multiple times. Each cycle the PRE-FIX code granted victim a fresh
	// burst on return.
	for cycle := 0; cycle < 5; cycle++ {
		for i := 0; i < maxKeys+1; i++ {
			l.Allow(fmt.Sprintf("attacker-%d-%d", cycle, i))
		}
		// Victim's entry has been evicted at least once this cycle.
		// Verify victim still cannot burst freely.
		if l.Allow("victim") {
			t.Fatalf("cycle %d: victim bucket was refreshed by eviction/re-insertion — thrash attack succeeded", cycle)
		}
	}
}

func TestKeyedLimiter_SnapshotCacheIsBounded(t *testing.T) {
	// The snapshot cache preserves evicted limiters but MUST itself be
	// bounded, else it becomes a new DoS surface. An adversary minting
	// N >> MaxKeys distinct IDs must not cause the snapshot cache to grow
	// without bound.
	const maxKeys = 4
	l := NewKeyedLimiter(KeyedLimiterConfig{
		Rate:        0.001,
		Burst:       1,
		MaxKeys:     maxKeys,
		AnonymousID: "anon",
	})
	defer l.Close()

	// Insert way more than maxKeys distinct keys — this will push many
	// entries through the snapshot cache.
	for i := 0; i < maxKeys*10; i++ {
		l.Allow(fmt.Sprintf("key-%d", i))
	}

	// Active map stays bounded (existing invariant).
	if got := l.Size(); got > maxKeys {
		t.Errorf("active Size() = %d, want <= %d", got, maxKeys)
	}
	// Snapshot cache must also stay bounded.
	if got := l.snapshotSize(); got > maxKeys {
		t.Errorf("snapshotSize() = %d, want <= %d", got, maxKeys)
	}
}

func TestKeyedLimiter_SnapshotRestorePreservesTokens(t *testing.T) {
	// Direct assertion that evict → re-insert preserves token count, not
	// just "denied on next call". Uses 1 victim + MaxKeys attackers so
	// the victim is exactly evicted, then reads back the limiter.
	const maxKeys = 2
	l := NewKeyedLimiter(KeyedLimiterConfig{
		Rate:        0.001,
		Burst:       5,
		MaxKeys:     maxKeys,
		AnonymousID: "anon",
	})
	defer l.Close()

	// Victim consumes 3 of 5 tokens.
	for i := 0; i < 3; i++ {
		if !l.Allow("victim") {
			t.Fatalf("victim call %d denied prematurely", i)
		}
	}

	// Force eviction of victim.
	l.Allow("attacker-A")
	l.Allow("attacker-B")

	// Victim returns. Remaining tokens should still be ~2 (not 5).
	// We verify by showing the next 2 calls succeed and the 3rd is denied.
	if !l.Allow("victim") {
		t.Error("victim should have 2 tokens remaining after restore, got denied (call 1 of remaining)")
	}
	if !l.Allow("victim") {
		t.Error("victim should have 1 token remaining after restore, got denied (call 2 of remaining)")
	}
	if l.Allow("victim") {
		t.Error("victim should be denied after consuming restored tokens — state was not preserved (got fresh burst)")
	}
}

func TestKeyedLimiter_ZeroConfigDefaults(t *testing.T) {
	// A zero-ish config should default to sensible values rather than panic
	// or allow-all. We require Rate>0 and Burst>0 and MaxKeys>0.
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("NewKeyedLimiter with zero config should not panic, got %v", r)
		}
	}()
	l := NewKeyedLimiter(KeyedLimiterConfig{})
	defer l.Close()

	// Must allow at least one call with a non-trivial default burst.
	if !l.Allow("k") {
		t.Error("zero-config limiter should allow at least the first call (non-zero default burst)")
	}
}
