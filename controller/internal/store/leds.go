package store

import (
	"context"
	"fmt"
)

// SetLEDSchedule stores a per-device daily status-LED window. An empty on/off
// time disables that edge and clears its due time. A new schedule is a fresh
// intent, so any repair backoff is reset.
func (s *Store) SetLEDSchedule(ctx context.Context, mac, onAt, offAt string, onNext, offNext int64) error {
	if onAt == "" {
		onNext = 0
	}
	if offAt == "" {
		offNext = 0
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE devices SET
			led_on_at = ?, led_off_at = ?, led_on_next_at = ?, led_off_next_at = ?,
			led_retry_count = 0, led_retry_at = 0
		  WHERE mac = ?`,
		onAt, offAt, onNext, offNext, mac)
	if err != nil {
		return fmt.Errorf("set LED schedule %s: %w", mac, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RecordLEDState stores a confirmed actual state. It comes from the AP's own
// hello report or from a completed led_set result.
func (s *Store) RecordLEDState(ctx context.Context, mac, state string, at int64) error {
	if state != "on" && state != "off" {
		return fmt.Errorf("record LED state %s: invalid state %q", mac, state)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE devices SET led_state = ?, led_state_at = ? WHERE mac = ?`,
		state, at, mac)
	if err != nil {
		return fmt.Errorf("record LED state %s: %w", mac, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DueLEDSchedules lists online, adopted devices with at least one LED edge
// due. onlineAfter is the same silence window the UI uses, so a schedule never
// fires at an AP that would only collect an expired action.
func (s *Store) DueLEDSchedules(ctx context.Context, now, onlineAfter int64, limit int) ([]Device, error) {
	if limit <= 0 || limit > 128 {
		limit = 32
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+deviceColumns+` FROM devices
		  WHERE state = ? AND (
		    (led_on_next_at > 0 AND led_on_next_at <= ?) OR
		    (led_off_next_at > 0 AND led_off_next_at <= ?)
		  ) AND last_seen >= ?
		  ORDER BY
		    CASE WHEN led_on_next_at > 0 THEN led_on_next_at ELSE 9223372036854775807 END,
		    CASE WHEN led_off_next_at > 0 THEN led_off_next_at ELSE 9223372036854775807 END
		  LIMIT ?`,
		StateAdopted, now, now, now-onlineAfter, limit)
	if err != nil {
		return nil, fmt.Errorf("list due LED schedules: %w", err)
	}
	defer rows.Close()

	var out []Device
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, fmt.Errorf("scan due LED schedule: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate due LED schedules: %w", err)
	}
	return out, nil
}

// AdvanceLEDSchedules moves both edges of a schedule past now in one write.
// Both are advanced even when only one was due: a device that returns after a
// long outage must not replay a stale sequence of opposite edges, which would
// leave the final state depending on command ordering.
func (s *Store) AdvanceLEDSchedules(ctx context.Context, mac string, onNext, offNext int64) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE devices SET led_on_next_at = ?, led_off_next_at = ? WHERE mac = ?`,
		onNext, offNext, mac)
	if err != nil {
		return fmt.Errorf("advance LED schedules %s: %w", mac, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListLEDWindows returns online, adopted devices with a complete on→off
// window. The patrol derives the expected state from the current time instead
// of from stored intent, so saving a schedule takes effect immediately and a
// reboot cannot leave a stale expectation behind.
func (s *Store) ListLEDWindows(ctx context.Context, now, onlineAfter int64, limit int) ([]Device, error) {
	if limit <= 0 || limit > 128 {
		limit = 32
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+deviceColumns+` FROM devices
		  WHERE state = ? AND last_seen >= ?
		    AND led_on_at <> '' AND led_off_at <> ''
		  ORDER BY mac
		  LIMIT ?`,
		StateAdopted, now-onlineAfter, limit)
	if err != nil {
		return nil, fmt.Errorf("list LED windows: %w", err)
	}
	defer rows.Close()

	var out []Device
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, fmt.Errorf("scan LED window: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate LED windows: %w", err)
	}
	return out, nil
}

// NoteLEDRepair records one repair attempt and when the next one may run.
func (s *Store) NoteLEDRepair(ctx context.Context, mac string, attempt int, nextAt int64) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE devices SET led_retry_count = ?, led_retry_at = ? WHERE mac = ?`,
		attempt, nextAt, mac)
	if err != nil {
		return fmt.Errorf("note LED repair %s: %w", mac, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ResetLEDRepair ends a repair cycle after the AP reached the scheduled state.
func (s *Store) ResetLEDRepair(ctx context.Context, mac string) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE devices SET led_retry_count = 0, led_retry_at = 0
		  WHERE mac = ? AND (led_retry_count <> 0 OR led_retry_at <> 0)`, mac); err != nil {
		return fmt.Errorf("reset LED repair %s: %w", mac, err)
	}
	return nil
}

// HasFreshAction reports whether a pending action of this type could still be
// executed. Actions past the dispatch deadline or the queue TTL are about to
// expire and must not block a new one: the whole point of the repair loop is
// to replace commands that were lost in transit.
func (s *Store) HasFreshAction(ctx context.Context, mac, typ string, now int64) (bool, error) {
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM device_actions
		  WHERE mac = ? AND type = ? AND state = ? AND (
		    (dispatched_at > 0 AND dispatched_at >= ?) OR
		    (dispatched_at = 0 AND created_at >= ?)
		  )`,
		mac, typ, ActionPending,
		now-int64(ActionDispatchTimeout.Seconds()), now-int64(ActionQueueTTL.Seconds())).Scan(&n); err != nil {
		return false, fmt.Errorf("check fresh action %s: %w", mac, err)
	}
	return n > 0, nil
}
