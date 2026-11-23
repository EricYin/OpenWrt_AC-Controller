package store

import (
	"context"
	"testing"
	"time"
)

func seedAdoptedDevice(t *testing.T, st *Store, mac string, now int64) {
	t.Helper()
	ctx := context.Background()
	if err := st.UpsertDiscovered(ctx, Device{MAC: mac, Model: "XR1710G"}); err != nil {
		t.Fatalf("seed device: %v", err)
	}
	if _, err := st.db.ExecContext(ctx,
		`UPDATE devices SET state = ?, last_seen = ? WHERE mac = ?`,
		StateAdopted, now, mac); err != nil {
		t.Fatalf("adopt device: %v", err)
	}
}

func TestDueLEDSchedulesRequiresOnlineDevice(t *testing.T) {
	st, ctx := newActionsStore(t)
	const mac = "8c:de:52:11:22:33"
	now := time.Now().Unix()
	seedAdoptedDevice(t, st, mac, now)

	if err := st.SetLEDSchedule(ctx, mac, "07:00", "23:00", now-60, now+3600); err != nil {
		t.Fatalf("set LED schedule: %v", err)
	}
	due, err := st.DueLEDSchedules(ctx, now, 90, 10)
	if err != nil {
		t.Fatalf("due LED schedules: %v", err)
	}
	if len(due) != 1 || due[0].MAC != mac || due[0].LEDOnAt != "07:00" || due[0].LEDOffAt != "23:00" {
		t.Fatalf("due = %+v, want the online device", due)
	}

	// Silent for an hour: the queued action would expire before the AP saw it.
	if _, err := st.db.ExecContext(ctx,
		`UPDATE devices SET last_seen = ? WHERE mac = ?`, now-3600, mac); err != nil {
		t.Fatalf("age device: %v", err)
	}
	if due, _ := st.DueLEDSchedules(ctx, now, 90, 10); len(due) != 0 {
		t.Errorf("offline device still due: %+v", due)
	}

	// Clearing both edges disables the schedule.
	if err := st.SetLEDSchedule(ctx, mac, "", "", 0, 0); err != nil {
		t.Fatalf("disable LED schedule: %v", err)
	}
	d, err := st.GetDevice(ctx, mac)
	if err != nil {
		t.Fatalf("get device: %v", err)
	}
	if d.LEDOnAt != "" || d.LEDOffAt != "" || d.LEDOnNextAt != 0 || d.LEDOffNextAt != 0 {
		t.Errorf("cleared schedule = %+v", d)
	}
}

func TestAdvanceLEDSchedulesMovesBothEdges(t *testing.T) {
	st, ctx := newActionsStore(t)
	const mac = "8c:de:52:11:22:33"
	now := time.Now().Unix()
	seedAdoptedDevice(t, st, mac, now)

	if err := st.SetLEDSchedule(ctx, mac, "07:00", "23:00", now-60, now-120); err != nil {
		t.Fatalf("set LED schedule: %v", err)
	}
	next := now + 3600
	if err := st.AdvanceLEDSchedules(ctx, mac, next, next+60); err != nil {
		t.Fatalf("advance schedules: %v", err)
	}

	d, err := st.GetDevice(ctx, mac)
	if err != nil {
		t.Fatalf("get device: %v", err)
	}
	if d.LEDOnNextAt != next || d.LEDOffNextAt != next+60 {
		t.Errorf("next times = %d/%d, want %d/%d", d.LEDOnNextAt, d.LEDOffNextAt, next, next+60)
	}
}

func TestRecordLEDState(t *testing.T) {
	st, ctx := newActionsStore(t)
	const mac = "8c:de:52:11:22:33"
	now := time.Now().Unix()
	seedAdoptedDevice(t, st, mac, now)

	if err := st.NoteLEDRepair(ctx, mac, 3, now+600); err != nil {
		t.Fatalf("note repair: %v", err)
	}
	if err := st.RecordLEDState(ctx, mac, "off", now+10); err != nil {
		t.Fatalf("record state: %v", err)
	}
	d, _ := st.GetDevice(ctx, mac)
	if d.LEDState != "off" || d.LEDStateAt != now+10 {
		t.Errorf("state = %+v, want off recorded", d)
	}
	// The backoff belongs to the patrol, not to the raw observation.
	if d.LEDRetryCount != 3 {
		t.Errorf("retry count = %d, want untouched 3", d.LEDRetryCount)
	}
	if err := st.RecordLEDState(ctx, mac, "blink", now); err == nil {
		t.Error("invalid state accepted")
	}
}

func TestListLEDWindowsFilters(t *testing.T) {
	st, ctx := newActionsStore(t)
	const mac = "8c:de:52:11:22:33"
	now := time.Now().Unix()
	seedAdoptedDevice(t, st, mac, now)

	// No schedule: nothing to enforce.
	if list, _ := st.ListLEDWindows(ctx, now, 90, 10); len(list) != 0 {
		t.Fatalf("windows without schedule = %+v", list)
	}

	// A single edge is not a window; it stays edge-triggered.
	if err := st.SetLEDSchedule(ctx, mac, "07:00", "", now+3600, 0); err != nil {
		t.Fatalf("set LED schedule: %v", err)
	}
	if list, _ := st.ListLEDWindows(ctx, now, 90, 10); len(list) != 0 {
		t.Errorf("single edge listed as a window: %+v", list)
	}

	if err := st.SetLEDSchedule(ctx, mac, "07:00", "21:30", now+3600, now+7200); err != nil {
		t.Fatalf("set LED window: %v", err)
	}
	list, _ := st.ListLEDWindows(ctx, now, 90, 10)
	if len(list) != 1 || list[0].MAC != mac {
		t.Fatalf("windows = %+v, want the scheduled device", list)
	}

	// An offline AP is waiting for its heartbeat, not for another command.
	if _, err := st.db.ExecContext(ctx, `UPDATE devices SET last_seen = ? WHERE mac = ?`, now-3600, mac); err != nil {
		t.Fatalf("age device: %v", err)
	}
	if list, _ := st.ListLEDWindows(ctx, now, 90, 10); len(list) != 0 {
		t.Errorf("offline device listed: %+v", list)
	}
}

func TestResetLEDRepair(t *testing.T) {
	st, ctx := newActionsStore(t)
	const mac = "8c:de:52:11:22:33"
	now := time.Now().Unix()
	seedAdoptedDevice(t, st, mac, now)

	if err := st.NoteLEDRepair(ctx, mac, 4, now+600); err != nil {
		t.Fatalf("note repair: %v", err)
	}
	if err := st.ResetLEDRepair(ctx, mac); err != nil {
		t.Fatalf("reset repair: %v", err)
	}
	d, _ := st.GetDevice(ctx, mac)
	if d.LEDRetryCount != 0 || d.LEDRetryAt != 0 {
		t.Errorf("retry = %d/%d, want cleared", d.LEDRetryCount, d.LEDRetryAt)
	}
}

func TestHasFreshActionIgnoresExpired(t *testing.T) {
	st, ctx := newActionsStore(t)
	const mac = "8c:de:52:11:22:33"
	now := time.Now().Unix()
	seedAdoptedDevice(t, st, mac, now)

	if _, err := st.CreateAction(ctx, mac, "led_set", map[string]string{"state": "on"}); err != nil {
		t.Fatalf("create action: %v", err)
	}
	fresh, err := st.HasFreshAction(ctx, mac, "led_set", now)
	if err != nil || !fresh {
		t.Fatalf("fresh = %v err = %v, want true", fresh, err)
	}

	// Older than the queue TTL: the action will expire and must not block a retry.
	if _, err := st.db.ExecContext(ctx,
		`UPDATE device_actions SET created_at = ? WHERE mac = ?`, now-25*3600, mac); err != nil {
		t.Fatalf("age action: %v", err)
	}
	if fresh, _ := st.HasFreshAction(ctx, mac, "led_set", now); fresh {
		t.Error("expired action still counted as fresh")
	}

	// Dispatched but without a result for longer than the dispatch timeout.
	if _, err := st.db.ExecContext(ctx,
		`UPDATE device_actions SET created_at = ?, dispatched_at = ? WHERE mac = ?`,
		now, now-11*60, mac); err != nil {
		t.Fatalf("age dispatched action: %v", err)
	}
	if fresh, _ := st.HasFreshAction(ctx, mac, "led_set", now); fresh {
		t.Error("stale dispatched action still counted as fresh")
	}
}
