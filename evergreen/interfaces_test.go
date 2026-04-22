package evergreen

import (
	"context"
	"errors"
	"testing"
)

// Compile-time assertions that the mock implementations below satisfy the
// exported interfaces. A future refactor that changes an interface signature
// will fail to build here, which is the intended early-warning for a
// breaking change to the adapter contract.
var (
	_ DocumentStore   = (*mockStore)(nil)
	_ RefreshExecutor = (*mockExecutor)(nil)
	_ ClaimsReader    = (*mockClaims)(nil)
	_ RateLimiter     = (*mockLimiter)(nil)
)

type mockStore struct{ m map[string]*Document }

func (s *mockStore) Save(_ context.Context, d *Document) error {
	if s.m == nil {
		s.m = map[string]*Document{}
	}
	s.m[d.ID] = d
	return nil
}
func (s *mockStore) Get(_ context.Context, id string) (*Document, error) {
	d, ok := s.m[id]
	if !ok {
		return nil, ErrNotFound
	}
	return d, nil
}
func (s *mockStore) List(_ context.Context) ([]*Document, error) {
	out := make([]*Document, 0, len(s.m))
	for _, d := range s.m {
		out = append(out, d)
	}
	return out, nil
}
func (s *mockStore) Delete(_ context.Context, id string) error {
	if _, ok := s.m[id]; !ok {
		return ErrNotFound
	}
	delete(s.m, id)
	return nil
}
func (s *mockStore) UpdateStatus(_ context.Context, id string, st DocStatus) error {
	d, ok := s.m[id]
	if !ok {
		return ErrNotFound
	}
	d.Status = st
	return nil
}

type mockExecutor struct{ name string }

func (m *mockExecutor) Refresh(_ context.Context, _ *Document) (RefreshResult, error) {
	return RefreshResult{Backend: m.name}, nil
}
func (m *mockExecutor) Name() string { return m.name }

type mockClaims struct{}

func (mockClaims) GetSymbol(_ context.Context, _ string, _ int64) (*SymbolState, error) {
	return nil, ErrSymbolNotFound
}
func (mockClaims) ResolveSymbolByLocation(_ context.Context, _, _ string, _, _ int) (int64, error) {
	return 0, ErrSymbolNotFound
}

type mockLimiter struct{ allow bool }

func (l *mockLimiter) Allow(_ context.Context, _ string) error {
	if !l.allow {
		return ErrRateLimited
	}
	return nil
}

// TestMockStore_NotFoundSemantics exercises the contract Get/Delete/UpdateStatus
// return ErrNotFound for missing documents.
func TestMockStore_NotFoundSemantics(t *testing.T) {
	s := &mockStore{}
	ctx := context.Background()
	// Use errors.Is throughout: the public contract is errors.Is-satisfiability,
	// not pointer equality. Adapters are expected to wrap; see TestSentinelErrors.
	if _, err := s.Get(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get missing: got %v, want ErrNotFound", err)
	}
	if err := s.Delete(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete missing: got %v, want ErrNotFound", err)
	}
	if err := s.UpdateStatus(ctx, "missing", FreshStatus); !errors.Is(err, ErrNotFound) {
		t.Errorf("UpdateStatus missing: got %v, want ErrNotFound", err)
	}
}

func TestMockLimiter_RateLimit(t *testing.T) {
	denied := &mockLimiter{allow: false}
	if err := denied.Allow(context.Background(), "doc-1"); !errors.Is(err, ErrRateLimited) {
		t.Errorf("Allow: got %v, want ErrRateLimited", err)
	}
	allowed := &mockLimiter{allow: true}
	if err := allowed.Allow(context.Background(), "doc-1"); err != nil {
		t.Errorf("Allow: got %v, want nil", err)
	}
}
