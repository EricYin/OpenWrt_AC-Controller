package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Package is one signed configuration revision exactly as it was handed out.
// The blob is kept verbatim so that "what did the AP actually download" stays
// answerable after the template has moved on.
type Package struct {
	MAC       string `json:"mac"`
	Rev       int    `json:"rev"`
	CreatedAt int64  `json:"created_at"`
	SHA256    string `json:"sha256"`
	Size      int    `json:"size"`
	// Files names the uci files this revision carried. It is recorded at build
	// time because the manifest inside the blob is not cheap to read back, and
	// it is what tells an operator whether a push touched the network file.
	Files []string `json:"files,omitempty"`
}

// CreatePackage stores a built revision. Revisions are immutable: attempting to
// store the same rev twice fails instead of silently overwriting history.
func (s *Store) CreatePackage(ctx context.Context, mac string, rev int, sha256 string, files []string, blob []byte) error {
	const q = `
INSERT INTO packages (mac, rev, created_at, sha256, size, files, blob)
VALUES (?, ?, strftime('%s','now'), ?, ?, ?, ?)`
	if _, err := s.db.ExecContext(ctx, q, mac, rev, sha256, len(blob), strings.Join(files, ","), blob); err != nil {
		return fmt.Errorf("store package %s rev %d: %w", mac, rev, err)
	}
	return nil
}

// CommitAdoption saves a revision and approves exactly the enrollment that was
// read before rendering. A reset in between leaves the new enrollment pending.
func (s *Store) CommitAdoption(ctx context.Context, d Device, templateID string, rev int, digest string, files []string, blob []byte) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE devices SET state = ?, template_id = ?, config_rev = ?, config_sha = ?, enrollment_released = 0
WHERE mac = ? AND enrollment_id = ? AND enrollment_public = ? AND config_rev = ? AND state = ?`,
		StateAdopted, templateID, rev, digest, d.MAC, d.EnrollmentID, d.EnrollmentPublic, d.ConfigRev, d.State)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrDeviceChanged
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO packages (mac, rev, created_at, sha256, size, files, blob)
VALUES (?, ?, strftime('%s','now'), ?, ?, ?, ?)`, d.MAC, rev, digest, len(blob), strings.Join(files, ","), blob); err != nil {
		return err
	}
	return tx.Commit()
}

// TrimPackages keeps only the newest keep revisions for one device. Zero means
// unlimited. The signed blobs are the largest rows in the database, so a
// flash-constrained deployment wants this at 1; a controller with room to spare
// leaves it at 0 and keeps the full audit trail.
func (s *Store) TrimPackages(ctx context.Context, mac string, keep int) error {
	if keep <= 0 {
		return nil
	}
	const q = `
DELETE FROM packages WHERE mac = ? AND rev NOT IN (
	SELECT rev FROM packages WHERE mac = ? ORDER BY rev DESC LIMIT ?)`
	if _, err := s.db.ExecContext(ctx, q, mac, mac, keep); err != nil {
		return fmt.Errorf("trim packages %s: %w", mac, err)
	}
	return nil
}

// LatestPackage returns the newest stored revision for a device.
func (s *Store) LatestPackage(ctx context.Context, mac string) (Package, []byte, error) {
	const q = `
SELECT rev, created_at, sha256, size, blob
FROM packages WHERE mac = ? ORDER BY rev DESC LIMIT 1`
	return s.scanPackage(s.read().QueryRowContext(ctx, q, mac))
}

// PackageByRev returns a specific revision.
func (s *Store) PackageByRev(ctx context.Context, mac string, rev int) (Package, []byte, error) {
	const q = `
SELECT rev, created_at, sha256, size, blob
FROM packages WHERE mac = ? AND rev = ?`
	return s.scanPackage(s.read().QueryRowContext(ctx, q, mac, rev))
}

func (s *Store) scanPackage(row *sql.Row) (Package, []byte, error) {
	var (
		p    Package
		blob []byte
	)
	err := row.Scan(&p.Rev, &p.CreatedAt, &p.SHA256, &p.Size, &blob)
	if errors.Is(err, sql.ErrNoRows) {
		return Package{}, nil, ErrNotFound
	}
	if err != nil {
		return Package{}, nil, err
	}
	return p, blob, nil
}

// ListPackages returns revision metadata without the blobs.
func (s *Store) ListPackages(ctx context.Context, mac string) ([]Package, error) {
	const q = `
SELECT rev, created_at, sha256, size, files
FROM packages WHERE mac = ? ORDER BY rev DESC`
	rows, err := s.read().QueryContext(ctx, q, mac)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Package, 0, 4)
	for rows.Next() {
		var files string
		p := Package{MAC: mac}
		if err := rows.Scan(&p.Rev, &p.CreatedAt, &p.SHA256, &p.Size, &files); err != nil {
			return nil, err
		}
		if files != "" {
			p.Files = strings.Split(files, ",")
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
