package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")

// Binding maps a physical tag to a Clover item.
type Binding struct {
	MAC          string
	ItemID       string
	ItemName     string // denormalized for UI; refreshed on catalog sync
	PriceCents   int64
	BridgeID     string // bridge reaching this tag ("" = none/fake)
	LastPushedAt *time.Time
	UpdatedAt    time.Time
}

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS bindings (
    mac            TEXT PRIMARY KEY,
    item_id        TEXT NOT NULL,
    item_name      TEXT NOT NULL DEFAULT '',
    price_cents    INTEGER NOT NULL DEFAULT 0,
    last_pushed_at TEXT,
    updated_at     TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_bindings_item ON bindings(item_id);
`)
	if err != nil {
		return err
	}
	return s.migrateBridges()
}

// Upsert inserts a binding or re-points an existing tag to another item.
func (s *Store) Upsert(ctx context.Context, b Binding) error {
	b.UpdatedAt = time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `
INSERT INTO bindings (mac, item_id, item_name, price_cents, bridge_id, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(mac) DO UPDATE SET
    item_id=excluded.item_id,
    item_name=excluded.item_name,
    price_cents=excluded.price_cents,
    bridge_id=excluded.bridge_id,
    updated_at=excluded.updated_at;
`, b.MAC, b.ItemID, b.ItemName, b.PriceCents, b.BridgeID, b.UpdatedAt.Format(time.RFC3339))
	return err
}

func (s *Store) Get(ctx context.Context, mac string) (Binding, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT mac, item_id, item_name, price_cents, bridge_id, last_pushed_at, updated_at
		 FROM bindings WHERE mac=?`, mac)
	return scanBinding(row)
}

func (s *Store) List(ctx context.Context) ([]Binding, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT mac, item_id, item_name, price_cents, bridge_id, last_pushed_at, updated_at
		 FROM bindings ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Binding
	for rows.Next() {
		b, err := scanBinding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ByItem returns all tags bound to an item.
func (s *Store) ByItem(ctx context.Context, itemID string) ([]Binding, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT mac, item_id, item_name, price_cents, bridge_id, last_pushed_at, updated_at
		 FROM bindings WHERE item_id=?`, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Binding
	for rows.Next() {
		b, err := scanBinding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) Delete(ctx context.Context, mac string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM bindings WHERE mac=?`, mac)
	return err
}

func (s *Store) MarkPushed(ctx context.Context, mac string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE bindings SET last_pushed_at=? WHERE mac=?`,
		time.Now().UTC().Format(time.RFC3339), mac)
	return err
}

type scanner interface{ Scan(dest ...any) error }

func scanBinding(sc scanner) (Binding, error) {
	var b Binding
	var lastPushed sql.NullString
	var updated string
	err := sc.Scan(&b.MAC, &b.ItemID, &b.ItemName, &b.PriceCents, &b.BridgeID, &lastPushed, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Binding{}, ErrNotFound
	}
	if err != nil {
		return Binding{}, err
	}
	if t, e := time.Parse(time.RFC3339, updated); e == nil {
		b.UpdatedAt = t
	}
	if lastPushed.Valid {
		if t, e := time.Parse(time.RFC3339, lastPushed.String); e == nil {
			b.LastPushedAt = &t
		}
	}
	return b, nil
}
