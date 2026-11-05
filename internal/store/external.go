package store

import (
	"context"
	"encoding/json"
	"time"
)

// ExternalTool is one installed third-party tool. Metadata only.
type ExternalTool struct {
	Name         string
	Version      string
	License      string
	State        string // "off" or "on"
	InstalledAt  time.Time
	LicenseAckAt time.Time
	ChangeIDs    []string
}

// ExternalTools persists installed external tools.
type ExternalTools struct{ db *DB }

// ExternalTools returns the external-tool store for d.
func (d *DB) ExternalTools() *ExternalTools { return &ExternalTools{db: d} }

// Put inserts or replaces a record.
func (e *ExternalTools) Put(ctx context.Context, t ExternalTool) error {
	ids := t.ChangeIDs
	if ids == nil {
		ids = []string{}
	}
	b, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	_, err = e.db.ExecContext(ctx,
		`INSERT INTO external_tools(name, version, license, state, installed_at, license_ack_at, changes)
		 VALUES(?,?,?,?,?,?,?)
		 ON CONFLICT(name) DO UPDATE SET version=excluded.version, license=excluded.license,
		   state=excluded.state, installed_at=excluded.installed_at,
		   license_ack_at=excluded.license_ack_at, changes=excluded.changes`,
		t.Name, t.Version, t.License, t.State, t.InstalledAt.UnixMilli(), t.LicenseAckAt.UnixMilli(), string(b))
	return err
}

// Get returns the record for name.
func (e *ExternalTools) Get(ctx context.Context, name string) (ExternalTool, bool, error) {
	rows, err := e.list(ctx, `WHERE name = ?`, name)
	if err != nil || len(rows) == 0 {
		return ExternalTool{}, false, err
	}
	return rows[0], true, nil
}

// List returns all records sorted by name.
func (e *ExternalTools) List(ctx context.Context) ([]ExternalTool, error) {
	return e.list(ctx, ``)
}

// SetState changes only the state; it reports false if the tool is unknown.
func (e *ExternalTools) SetState(ctx context.Context, name, state string) (bool, error) {
	res, err := e.db.ExecContext(ctx, `UPDATE external_tools SET state = ? WHERE name = ?`, state, name)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// Delete removes the record. Deleting an unknown name is not an error.
func (e *ExternalTools) Delete(ctx context.Context, name string) error {
	_, err := e.db.ExecContext(ctx, `DELETE FROM external_tools WHERE name = ?`, name)
	return err
}

func (e *ExternalTools) list(ctx context.Context, where string, args ...any) ([]ExternalTool, error) {
	rows, err := e.db.QueryContext(ctx,
		`SELECT name, version, license, state, installed_at, license_ack_at, changes FROM external_tools `+where+` ORDER BY name`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ExternalTool
	for rows.Next() {
		var t ExternalTool
		var inst, ack int64
		var ch string
		if err := rows.Scan(&t.Name, &t.Version, &t.License, &t.State, &inst, &ack, &ch); err != nil {
			return nil, err
		}
		t.InstalledAt = time.UnixMilli(inst).UTC()
		t.LicenseAckAt = time.UnixMilli(ack).UTC()
		if err := json.Unmarshal([]byte(ch), &t.ChangeIDs); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
