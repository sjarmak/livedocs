package semantic

import (
	"context"
	"fmt"
)

// BatchResult summarises a batch run across multiple packages.
type BatchResult struct {
	Packages     []PackageResult
	TotalClaims  int
	TotalErrors  int
	TotalSkipped int // packages with no structural claims
}

// GenerateBatch processes multiple import paths sequentially, collecting
// results. It does not stop on individual package errors.
func (g *Generator) GenerateBatch(ctx context.Context, importPaths []string) (BatchResult, error) {
	var br BatchResult
	br.Packages = make([]PackageResult, 0, len(importPaths))

	for _, path := range importPaths {
		select {
		case <-ctx.Done():
			return br, ctx.Err()
		default:
		}

		pr, err := g.GenerateForPackage(ctx, path)
		if err != nil {
			pr.Err = err
			br.TotalErrors++
		}
		if pr.ClaimsStored == 0 && pr.Err == nil {
			br.TotalSkipped++
		}
		br.TotalClaims += pr.ClaimsStored
		br.Packages = append(br.Packages, pr)
	}

	return br, nil
}

// GenerateBatchFromDB queries the database for distinct import paths and
// processes up to limit packages.
func (g *Generator) GenerateBatchFromDB(ctx context.Context, limit int) (BatchResult, error) {
	paths, err := g.cfg.ClaimsDB.ListDistinctImportPaths(limit)
	if err != nil {
		return BatchResult{}, fmt.Errorf("list import paths: %w", err)
	}
	return g.GenerateBatch(ctx, paths)
}
