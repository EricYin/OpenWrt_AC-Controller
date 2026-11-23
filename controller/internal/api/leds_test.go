package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/naoki66/ac-controller/controller/internal/store"
	"github.com/naoki66/ac-controller/shared/proto"
)

func TestLEDScheduleValidation(t *testing.T) {
	f := newFixture(t)

	// Pending devices are not managed yet, so they have no schedule to set.
	resp := f.do(http.MethodPut, "/api/v1/devices/"+testMAC+"/led-schedule",
		map[string]string{"on_at": "07:00", "off_at": "23:00"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("pending status = %d, want 409", resp.StatusCode)
	}

	resp = f.do(http.MethodPut, "/api/v1/devices/not-a-mac/led-schedule",
		map[string]string{"on_at": "07:00"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed MAC status = %d, want 400", resp.StatusCode)
	}

	f.adopt()

	resp = f.do(http.MethodPut, "/api/v1/devices/"+testMAC+"/led-schedule",
		map[string]string{"on_at": "7am", "off_at": "23:00"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad time status = %d, want 400", resp.StatusCode)
	}

	resp = f.do(http.MethodPut, "/api/v1/devices/"+testMAC+"/led-schedule",
		map[string]string{"on_at": "07:00", "off_at": "07:00"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("equal times status = %d, want 400", resp.StatusCode)
	}

	d := decode[store.Device](t, f.do(http.MethodPut,
		"/api/v1/devices/"+testMAC+"/led-schedule",
		map[string]string{"on_at": "07:00", "off_at": "23:00"}))
	if d.LEDOnAt != "07:00" || d.LEDOffAt != "23:00" {
		t.Errorf("schedule = %+v", d)
	}
	if d.LEDOnNextAt <= time.Now().Unix() || d.LEDOffNextAt <= time.Now().Unix() {
		t.Errorf("next times = %d/%d, want in the future", d.LEDOnNextAt, d.LEDOffNextAt)
	}

	// Empty times disable the schedule and clear the due times.
	d = decode[store.Device](t, f.do(http.MethodPut,
		"/api/v1/devices/"+testMAC+"/led-schedule", map[string]string{}))
	if d.LEDOnAt != "" || d.LEDOffAt != "" || d.LEDOnNextAt != 0 || d.LEDOffNextAt != 0 {
		t.Errorf("cleared schedule = %+v", d)
	}
}

func TestLEDActionValidation(t *testing.T) {
	f := newFixture(t)
	f.adopt()

	resp := f.do(http.MethodPost, "/api/v1/devices/"+testMAC+"/actions",
		map[string]any{"type": proto.ActionLEDSet, "args": map[string]string{"state": "on"}})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("led_set status = %d, want 202", resp.StatusCode)
	}
	created := decode[store.Action](t, resp)
	if created.Type != proto.ActionLEDSet || created.Args["state"] != "on" {
		t.Errorf("created action = %+v", created)
	}
	resp.Body.Close()

	resp = f.do(http.MethodPost, "/api/v1/devices/"+testMAC+"/actions",
		map[string]any{"type": proto.ActionLEDSet, "args": map[string]string{"state": "blink"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad state status = %d, want 400", resp.StatusCode)
	}
}

func TestRunDueLEDsQueuesOnce(t *testing.T) {
	f := newFixture(t)
	f.adopt()
	ctx := context.Background()
	now := time.Now().Unix()
	if err := f.store.SetLEDSchedule(ctx, testMAC, "07:00", "", now-60, 0); err != nil {
		t.Fatalf("set LED schedule: %v", err)
	}

	f.srv.runDueLEDs(ctx)
	list, err := f.store.ListActions(ctx, testMAC, 10)
	if err != nil {
		t.Fatalf("list actions: %v", err)
	}
	if len(list) != 1 || list[0].Type != proto.ActionLEDSet || list[0].State != store.ActionPending {
		t.Fatalf("actions = %+v, want one pending led_set", list)
	}
	if list[0].Args["state"] != "on" {
		t.Errorf("led_set args = %+v, want state on", list[0].Args)
	}

	d, err := f.store.GetDevice(ctx, testMAC)
	if err != nil {
		t.Fatalf("get device: %v", err)
	}
	if d.LEDOnNextAt <= now {
		t.Errorf("next_at = %d, want advanced past %d", d.LEDOnNextAt, now)
	}

	// The schedule must not fire twice for the same occurrence.
	f.srv.runDueLEDs(ctx)
	list, _ = f.store.ListActions(ctx, testMAC, 10)
	if len(list) != 1 {
		t.Errorf("actions after second pass = %+v, want one", list)
	}
}

func TestRunDueLEDsSkipsWhenOneIsPending(t *testing.T) {
	f := newFixture(t)
	f.adopt()
	ctx := context.Background()
	now := time.Now().Unix()

	if _, err := f.store.CreateAction(ctx, testMAC, proto.ActionLEDSet, map[string]string{"state": "off"}); err != nil {
		t.Fatalf("create manual action: %v", err)
	}
	if err := f.store.SetLEDSchedule(ctx, testMAC, "07:00", "", now-60, 0); err != nil {
		t.Fatalf("set LED schedule: %v", err)
	}

	f.srv.runDueLEDs(ctx)
	list, _ := f.store.ListActions(ctx, testMAC, 10)
	if len(list) != 1 {
		t.Errorf("actions = %+v, want the manual one only", list)
	}
	// The due time still advances, otherwise the next tick would keep retrying.
	d, _ := f.store.GetDevice(ctx, testMAC)
	if d.LEDOnNextAt <= now {
		t.Errorf("next_at = %d, want advanced", d.LEDOnNextAt)
	}
}

func TestLEDWindowState(t *testing.T) {
	day := func(h, m int) time.Time {
		return time.Date(2026, 11, 24, h, m, 0, 0, time.Local)
	}
	cases := []struct {
		name    string
		now     time.Time
		on, off string
		want    string
	}{
		{"inside same-day window", day(12, 0), "07:00", "22:00", "on"},
		{"before same-day window", day(6, 0), "07:00", "22:00", "off"},
		{"after same-day window", day(23, 0), "07:00", "22:00", "off"},
		{"inside overnight window", day(23, 30), "22:00", "07:00", "on"},
		{"early morning overnight window", day(3, 0), "22:00", "07:00", "on"},
		{"outside overnight window", day(12, 0), "22:00", "07:00", "off"},
	}
	for _, tc := range cases {
		got, ok := ledWindowState(tc.now, tc.on, tc.off)
		if !ok || got != tc.want {
			t.Errorf("%s: state = %q ok=%v, want %q", tc.name, got, ok, tc.want)
		}
	}
	if _, ok := ledWindowState(day(12, 0), "7am", "22:00"); ok {
		t.Error("bad clock reported a state")
	}
}

// Saving a window reconciles immediately instead of waiting for the patrol.
func TestSetLEDScheduleAppliesImmediately(t *testing.T) {
	f := newFixture(t)
	f.adopt()
	ctx := context.Background()
	now := time.Now()
	onAt := now.Add(-time.Hour).Format("15:04")
	offAt := now.Add(time.Hour).Format("15:04")
	if err := f.store.RecordLEDState(ctx, testMAC, "off", now.Unix()); err != nil {
		t.Fatalf("record state: %v", err)
	}

	resp := f.do(http.MethodPut, "/api/v1/devices/"+testMAC+"/led-schedule",
		map[string]string{"on_at": onAt, "off_at": offAt})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("schedule status = %d, want 200", resp.StatusCode)
	}

	list, err := f.store.ListActions(ctx, testMAC, 10)
	if err != nil {
		t.Fatalf("list actions: %v", err)
	}
	if len(list) != 1 || list[0].Type != proto.ActionLEDSet || list[0].Args["state"] != "on" {
		t.Fatalf("actions = %+v, want an immediate led_set on", list)
	}
	d, _ := f.store.GetDevice(ctx, testMAC)
	if d.LEDRetryCount != 1 {
		t.Errorf("retry = %d, want one immediate attempt", d.LEDRetryCount)
	}
}

// A device that was offline across both edges must land on the state of the
// current window with one command, not replay the stale on/off pair.
func TestRunDueLEDsCatchUpUsesCurrentWindow(t *testing.T) {
	f := newFixture(t)
	f.adopt()
	ctx := context.Background()
	now := time.Now()
	onAt := now.Add(-time.Hour).Format("15:04")
	offAt := now.Add(time.Hour).Format("15:04")
	if err := f.store.SetLEDSchedule(ctx, testMAC, onAt, offAt, now.Add(-30*time.Minute).Unix(), now.Add(-10*time.Minute).Unix()); err != nil {
		t.Fatalf("set LED schedule: %v", err)
	}

	f.srv.runDueLEDs(ctx)
	list, err := f.store.ListActions(ctx, testMAC, 10)
	if err != nil {
		t.Fatalf("list actions: %v", err)
	}
	if len(list) != 1 || list[0].Type != proto.ActionLEDSet {
		t.Fatalf("actions = %+v, want one led_set", list)
	}
	if list[0].Args["state"] != "on" {
		t.Errorf("led_set args = %+v, want the current window state on", list[0].Args)
	}

	d, err := f.store.GetDevice(ctx, testMAC)
	if err != nil {
		t.Fatalf("get device: %v", err)
	}
	if d.LEDOnNextAt <= now.Unix() || d.LEDOffNextAt <= now.Unix() {
		t.Errorf("next times = %d/%d, want both in the future", d.LEDOnNextAt, d.LEDOffNextAt)
	}
}

// Saving a window takes effect on the next patrol: the current window state is
// enforced immediately, without waiting for the next edge.
func TestLEDPatrolEnforcesCurrentWindow(t *testing.T) {
	f := newFixture(t)
	f.adopt()
	ctx := context.Background()
	now := time.Now()
	onAt := now.Add(-time.Hour).Format("15:04")
	offAt := now.Add(time.Hour).Format("15:04")
	if err := f.store.SetLEDSchedule(ctx, testMAC, onAt, offAt, now.Add(23*time.Hour).Unix(), now.Add(24*time.Hour).Unix()); err != nil {
		t.Fatalf("set LED schedule: %v", err)
	}
	if err := f.store.RecordLEDState(ctx, testMAC, "off", now.Unix()); err != nil {
		t.Fatalf("record state: %v", err)
	}

	f.srv.runLEDRepairs(ctx)
	list, err := f.store.ListActions(ctx, testMAC, 10)
	if err != nil {
		t.Fatalf("list actions: %v", err)
	}
	if len(list) != 1 || list[0].Args["state"] != "on" {
		t.Fatalf("actions = %+v, want one led_set on for the current window", list)
	}
	d, _ := f.store.GetDevice(ctx, testMAC)
	if d.LEDRetryCount != 1 || d.LEDRetryAt <= now.Unix() {
		t.Errorf("retry = %d/%d, want one attempt with a future backoff", d.LEDRetryCount, d.LEDRetryAt)
	}

	// The queued command can still run: do not stack another one.
	f.srv.runLEDRepairs(ctx)
	if list, _ := f.store.ListActions(ctx, testMAC, 10); len(list) != 1 {
		t.Errorf("actions after second pass = %+v, want one", list)
	}

	// The AP reports the state the window implies: the patrol stops and clears
	// the backoff.
	if err := f.store.RecordLEDState(ctx, testMAC, "on", now.Add(30*time.Second).Unix()); err != nil {
		t.Fatalf("record match: %v", err)
	}
	f.srv.runLEDRepairs(ctx)
	if list, _ := f.store.ListActions(ctx, testMAC, 10); len(list) != 1 {
		t.Errorf("actions after confirmation = %+v, want one", list)
	}
	d, _ = f.store.GetDevice(ctx, testMAC)
	if d.LEDRetryCount != 0 || d.LEDRetryAt != 0 {
		t.Errorf("retry after confirmation = %d/%d, want cleared", d.LEDRetryCount, d.LEDRetryAt)
	}
}

func TestLEDPatrolSkipsOfflineDevice(t *testing.T) {
	f := newFixture(t)
	f.adopt()
	ctx := context.Background()
	now := time.Now()
	// The window excludes the jumped clock used below, so a correct device
	// would be repaired if it were online.
	onAt := now.Add(-time.Hour).Format("15:04")
	offAt := now.Add(time.Hour).Format("15:04")
	if err := f.store.SetLEDSchedule(ctx, testMAC, onAt, offAt, 0, 0); err != nil {
		t.Fatalf("set LED schedule: %v", err)
	}
	if err := f.store.RecordLEDState(ctx, testMAC, "on", now.Unix()); err != nil {
		t.Fatalf("record state: %v", err)
	}
	// Jump the controller clock well past the silence window: the seeded
	// last_seen is now hours old, which is what an unreachable AP looks like.
	f.srv.Now = func() time.Time { return time.Now().Add(2 * time.Hour) }

	f.srv.runLEDRepairs(ctx)
	if list, _ := f.store.ListActions(ctx, testMAC, 10); len(list) != 0 {
		t.Errorf("actions = %+v, want none for an offline AP", list)
	}
}

func TestHelloRecordsLEDState(t *testing.T) {
	f := newFixture(t)
	f.adopt()

	resp := f.do(http.MethodPost, "/api/v1/ap/hello", proto.Hello{
		MAC:      testMAC,
		Model:    "XR1710G",
		Firmware: "20261026",
		Hostname: "ap250",
		LEDState: "off",
	})
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("hello status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	d, err := f.store.GetDevice(context.Background(), testMAC)
	if err != nil {
		t.Fatalf("get device: %v", err)
	}
	if d.LEDState != "off" || d.LEDStateAt == 0 {
		t.Errorf("reported LED state = %+v, want off", d)
	}
}

func TestLEDResultRecordsState(t *testing.T) {
	f := newFixture(t)
	f.adopt()
	ctx := context.Background()

	a, err := f.store.CreateAction(ctx, testMAC, proto.ActionLEDSet, map[string]string{"state": "on"})
	if err != nil {
		t.Fatalf("create action: %v", err)
	}
	f.helloWith([]proto.ActionResult{{
		ID:    a.ID,
		Type:  proto.ActionLEDSet,
		State: proto.ActionResultDone,
	}})

	d, err := f.store.GetDevice(ctx, testMAC)
	if err != nil {
		t.Fatalf("get device: %v", err)
	}
	if d.LEDState != "on" {
		t.Errorf("state = %q, want on from the done result", d.LEDState)
	}
}
