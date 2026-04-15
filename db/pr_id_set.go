package db

import (
	"encoding/binary"
	"fmt"
	"sort"
)

// PRMinerVersionNeedsRemine is the sentinel value written to
// source_files.pr_miner_version when a file must be re-mined from
// scratch on the next pass (e.g. after a cursor regression).
const PRMinerVersionNeedsRemine = "needs_remine"

// EncodePRIDSet encodes a slice of PR numbers into a compact binary blob.
//
// Encoding: sorted, deduped, little-endian int32. A slice of 10 PR numbers
// therefore occupies exactly 40 bytes. Negative values are rejected and
// values above math.MaxInt32 are clamped (callers should only store real
// PR numbers which fit easily in int32).
func EncodePRIDSet(ids []int) []byte {
	if len(ids) == 0 {
		return nil
	}
	// Copy, sort, dedupe.
	cp := make([]int, 0, len(ids))
	for _, v := range ids {
		if v < 0 {
			continue
		}
		cp = append(cp, v)
	}
	sort.Ints(cp)
	// Dedupe in place.
	out := cp[:0]
	var last int = -1
	for _, v := range cp {
		if v == last {
			continue
		}
		out = append(out, v)
		last = v
	}

	buf := make([]byte, 4*len(out))
	for i, v := range out {
		binary.LittleEndian.PutUint32(buf[i*4:], uint32(v))
	}
	return buf
}

// DecodePRIDSet decodes a blob produced by EncodePRIDSet.
// Returns nil for a nil or empty input. Returns an error if the blob
// length is not a multiple of 4.
func DecodePRIDSet(blob []byte) ([]int, error) {
	if len(blob) == 0 {
		return nil, nil
	}
	if len(blob)%4 != 0 {
		return nil, fmt.Errorf("decode pr id set: blob length %d not multiple of 4", len(blob))
	}
	n := len(blob) / 4
	out := make([]int, n)
	for i := 0; i < n; i++ {
		out[i] = int(binary.LittleEndian.Uint32(blob[i*4:]))
	}
	return out, nil
}

// GetPRIDSet returns the stored PR cursor for a source file, along with the
// pr_miner_version string. Missing source_files rows return (nil, "", nil).
// A NULL last_pr_id_set column returns (nil, version, nil).
func (c *ClaimsDB) GetPRIDSet(repo, relativePath string) ([]int, string, error) {
	var blob []byte
	var version string
	err := c.exec.QueryRow(`
		SELECT COALESCE(last_pr_id_set, X''), COALESCE(pr_miner_version, '')
		FROM source_files
		WHERE repo = ? AND relative_path = ?
	`, repo, relativePath).Scan(&blob, &version)
	if err != nil {
		return nil, "", err
	}
	ids, decodeErr := DecodePRIDSet(blob)
	if decodeErr != nil {
		return nil, version, fmt.Errorf("get pr id set for %s/%s: %w", repo, relativePath, decodeErr)
	}
	return ids, version, nil
}

// SetPRIDSet writes the PR cursor and miner version for a source file.
// The source_files row must already exist (callers should ensure the file
// is tracked, e.g. by UpsertSourceFile, or use ensureSourceFileRow helper
// below). Returns an error if no row matches.
func (c *ClaimsDB) SetPRIDSet(repo, relativePath string, ids []int, version string) error {
	blob := EncodePRIDSet(ids)
	if err := c.ensureSourceFileRow(repo, relativePath); err != nil {
		return fmt.Errorf("set pr id set: %w", err)
	}
	result, err := c.exec.Exec(`
		UPDATE source_files
		SET last_pr_id_set = ?, pr_miner_version = ?
		WHERE repo = ? AND relative_path = ?
	`, blob, version, repo, relativePath)
	if err != nil {
		return fmt.Errorf("set pr id set: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("set pr id set: check rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("set pr id set: source file not found: %s/%s", repo, relativePath)
	}
	return nil
}

// ClearPRIDSet wipes last_pr_id_set and pr_miner_version for every source
// file owned by the given repo. Used by the --force-remine CLI flag so the
// next run does a full re-mine.
func (c *ClaimsDB) ClearPRIDSet(repo string) error {
	_, err := c.exec.Exec(`
		UPDATE source_files
		SET last_pr_id_set = NULL, pr_miner_version = ''
		WHERE repo = ?
	`, repo)
	if err != nil {
		return fmt.Errorf("clear pr id set: %w", err)
	}
	return nil
}

// MarkNeedsRemine flips a file's pr_miner_version to the "needs_remine"
// sentinel and clears its cursor. The next miner pass will treat it as a
// full re-mine. Used when a cursor regression is detected.
func (c *ClaimsDB) MarkNeedsRemine(repo, relativePath string) error {
	if err := c.ensureSourceFileRow(repo, relativePath); err != nil {
		return fmt.Errorf("mark needs remine: %w", err)
	}
	_, err := c.exec.Exec(`
		UPDATE source_files
		SET last_pr_id_set = NULL, pr_miner_version = ?
		WHERE repo = ? AND relative_path = ?
	`, PRMinerVersionNeedsRemine, repo, relativePath)
	if err != nil {
		return fmt.Errorf("mark needs remine: %w", err)
	}
	return nil
}

// ensureSourceFileRow guarantees that a source_files row exists for
// (repo, relativePath). If one is already present, it is left alone. If
// absent, a minimal placeholder row is inserted so that subsequent
// per-column updates (PR cursor, miner version) have a target to land on.
//
// The placeholder carries a zero content_hash and an empty
// extractor_version string. Real structural extraction will upsert the row
// with proper values later; the placeholder is only a scaffold for the PR
// miner's per-file metadata.
func (c *ClaimsDB) ensureSourceFileRow(repo, relativePath string) error {
	_, err := c.exec.Exec(`
		INSERT INTO source_files (repo, relative_path, content_hash, extractor_version, grammar_version, last_indexed, deleted)
		VALUES (?, ?, '', '', NULL, ?, 0)
		ON CONFLICT(repo, relative_path) DO NOTHING
	`, repo, relativePath, Now())
	if err != nil {
		return fmt.Errorf("ensure source file row: %w", err)
	}
	return nil
}
