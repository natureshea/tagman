package store

import (
	"context"
	"database/sql"
	"time"
)

// Bridge is a network gateway (ESP32) that reaches tags over BLE. The service
// POSTs images to its HTTP server.
type Bridge struct {
	ID        string
	Name      string
	Address   string // host:port of the bridge's HTTP server
	LastSeen  *time.Time
	Healthy   bool
	CreatedAt time.Time
}

func (s *Store) migrateBridges() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS bridges (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL DEFAULT '',
    address    TEXT NOT NULL,
    last_seen  TEXT,
    healthy    INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL
);`)
	if err != nil {
		return err
	}
	// Add bridge_id to bindings if missing.
	_, _ = s.db.Exec(`ALTER TABLE bindings ADD COLUMN bridge_id TEXT NOT NULL DEFAULT ''`)
	return nil
}

func (s *Store) AddBridge(ctx context.Context, b Bridge) error {
	if b.CreatedAt.IsZero() {
		b.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO bridges (id, name, address, healthy, created_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET name=excluded.name, address=excluded.address;`,
		b.ID, b.Name, b.Address, boolToInt(b.Healthy), b.CreatedAt.Format(time.RFC3339))
	return err
}

func (s *Store) ListBridges(ctx context.Context) ([]Bridge, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, address, last_seen, healthy, created_at FROM bridges ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Bridge
	for rows.Next() {
		b, err := scanBridge(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) GetBridge(ctx context.Context, id string) (Bridge, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, address, last_seen, healthy, created_at FROM bridges WHERE id=?`, id)
	return scanBridge(row)
}

func (s *Store) DeleteBridge(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM bridges WHERE id=?`, id)
	return err
}

func (s *Store) SetBridgeHealth(ctx context.Context, id string, healthy bool, seen time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE bridges SET healthy=?, last_seen=? WHERE id=?`,
		boolToInt(healthy), seen.UTC().Format(time.RFC3339), id)
	return err
}

func scanBridge(sc scanner) (Bridge, error) {
	var b Bridge
	var lastSeen sql.NullString
	var created string
	var healthy int
	err := sc.Scan(&b.ID, &b.Name, &b.Address, &lastSeen, &healthy, &created)
	if err != nil {
		return Bridge{}, err
	}
	b.Healthy = healthy != 0
	if t, e := time.Parse(time.RFC3339, created); e == nil {
		b.CreatedAt = t
	}
	if lastSeen.Valid && lastSeen.String != "" {
		if t, e := time.Parse(time.RFC3339, lastSeen.String); e == nil {
			b.LastSeen = &t
		}
	}
	return b, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
