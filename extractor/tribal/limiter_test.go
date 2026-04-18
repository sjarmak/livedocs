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

	// Insert a new session — this MUST evict the oldest (sess-1).
	if !l.Allow("sess-5") {
		t.Fatal("sess-5 first call should succeed")
	}
	if got := l.Size(); got != maxKeys {
		t.Fatalf("after eviction, Size() = %d, want %d", got, maxKeys)
	}

	// sess-1 was evicted — it now has a FRESH bucket (test: should be allowed once).
	if !l.Allow("sess-1") {
		t.Error("sess-1 should have a fresh bucket after LRU eviction (but got denied, indicating no eviction occurred)")
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
