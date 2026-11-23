package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/naoki66/ac-controller/controller/internal/discovery"
	"github.com/naoki66/ac-controller/controller/internal/store"
	"github.com/naoki66/ac-controller/shared/proto"
)

// handleSetLEDSchedule stores a per-AP daily status-LED policy. The controller
// only declares intent here; the actual switch goes through the same action
// queue as a manual one, so an offline AP picks it up on return.
func (s *Server) handleSetLEDSchedule(w http.ResponseWriter, r *http.Request) {
	mac := discovery.NormalizeMAC(r.PathValue("mac"))
	if mac == "" {
		writeError(w, http.StatusBadRequest, errors.New("malformed MAC address"))
		return
	}
	device, err := s.Store.GetDevice(r.Context(), mac)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, errors.New("device not found"))
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if device.State != store.StateAdopted {
		writeError(w, http.StatusConflict, errors.New("device is not adopted"))
		return
	}

	var req struct {
		OnAt  string `json:"on_at"`
		OffAt string `json:"off_at"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	onAt := strings.TrimSpace(req.OnAt)
	offAt := strings.TrimSpace(req.OffAt)
	if onAt != "" && offAt != "" && onAt == offAt {
		writeError(w, http.StatusBadRequest, errors.New("on_at and off_at must differ"))
		return
	}

	now := s.now()
	onNext, offNext := int64(0), int64(0)
	if onAt != "" {
		when, err := nextClockOccurrence(now, onAt)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		onNext = when.Unix()
	}
	if offAt != "" {
		when, err := nextClockOccurrence(now, offAt)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		offNext = when.Unix()
	}

	if err := s.Store.SetLEDSchedule(r.Context(), mac, onAt, offAt, onNext, offNext); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.Store.LogEvent(r.Context(), "led_schedule", mac, formatLEDSchedule(onAt, offAt))

	device, err = s.Store.GetDevice(r.Context(), mac)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	// Apply the new window right away: an operator saving 07:00–21:30 at 02:00
	// expects the LED to go dark now, not after the first edge.
	s.reconcileLED(r.Context(), device)
	writeJSON(w, http.StatusOK, device)
}

func formatLEDSchedule(onAt, offAt string) string {
	if onAt == "" && offAt == "" {
		return "disabled"
	}
	parts := make([]string, 0, 2)
	if onAt != "" {
		parts = append(parts, "on "+onAt)
	}
	if offAt != "" {
		parts = append(parts, "off "+offAt)
	}
	return strings.Join(parts, ", ")
}

// ledRetryBackoff caps how often a drifting AP is retried. The first attempts
// are quick so a one-off packet loss is repaired within minutes; the cap keeps a
// permanently broken AP (no status LED, unreachable) from queueing a command
// every minute for the rest of the day.
var ledRetryBackoff = []time.Duration{
	time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute, 30 * time.Minute,
}

// ledRepairWarnAfter is the attempt count the UI shows as 整改异常.
const ledRepairWarnAfter = 5

func ledRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > len(ledRetryBackoff) {
		return ledRetryBackoff[len(ledRetryBackoff)-1]
	}
	return ledRetryBackoff[attempt-1]
}

// RunLEDScheduler fires due LED schedules and repairs drifted state once a
// minute until ctx is cancelled.
func (s *Server) RunLEDScheduler(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	s.runDueLEDs(ctx)
	s.runLEDRepairs(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runDueLEDs(ctx)
			s.runLEDRepairs(ctx)
		}
	}
}

func (s *Server) runDueLEDs(ctx context.Context) {
	now := s.now()
	devices, err := s.Store.DueLEDSchedules(ctx, now.Unix(), int64(s.offlineAfter()/time.Second), 32)
	if err != nil {
		s.Log.Warn("LED schedule query failed", "err", err)
		return
	}
	for _, d := range devices {
		target, ok := ledDueTarget(d, d.LEDOnNextAt <= now.Unix(), d.LEDOffNextAt <= now.Unix(), now)
		if !ok {
			continue
		}
		// Move the due time first. Whether the switch is issued or deliberately
		// skipped, the next occurrence must be in the future or every tick would
		// re-queue the same schedule.
		onNext, offNext, ok := advanceLEDClocks(d, now)
		if !ok {
			s.Log.Warn("LED schedule has a bad clock", "mac", d.MAC, "on", d.LEDOnAt, "off", d.LEDOffAt)
			continue
		}
		if err := s.Store.AdvanceLEDSchedules(ctx, d.MAC, onNext, offNext); err != nil {
			s.Log.Warn("LED schedules not advanced", "mac", d.MAC, "err", err)
			continue
		}

		pending, err := s.Store.HasFreshAction(ctx, d.MAC, proto.ActionLEDSet, now.Unix())
		if err != nil {
			s.Log.Warn("pending LED action check failed", "mac", d.MAC, "err", err)
			continue
		}
		if pending {
			// An operator already asked for a switch; stacking another would
			// apply two opposite states back to back.
			s.Log.Info("scheduled LED skipped: one is already pending", "mac", d.MAC)
			continue
		}

		if d.LEDState == target {
			// The AP is already where the schedule wants it (it may have stayed
			// on through the edge); no command is needed.
			continue
		}

		a, err := s.Store.CreateAction(ctx, d.MAC, proto.ActionLEDSet, map[string]string{"state": target})
		if err != nil {
			s.Log.Warn("scheduled LED not queued", "mac", d.MAC, "err", err)
			continue
		}
		s.Store.LogEvent(ctx, "led_scheduled", d.MAC, a.ID+" "+target)
		s.Log.Info("scheduled LED queued", "mac", d.MAC, "id", a.ID, "state", target)
	}
}

// runLEDRepairs is the patrol pass. For every online AP with a complete on→off
// window it derives the state that window implies right now and compares it
// with the last confirmed hardware state. A mismatch is corrected immediately;
// repeat failures back off. There is no stored "desired" state to go stale:
// saving a window is enough, and a reboot or power cut is corrected on the next
// pass.
func (s *Server) runLEDRepairs(ctx context.Context) {
	now := s.now()
	devices, err := s.Store.ListLEDWindows(ctx, now.Unix(), int64(s.offlineAfter()/time.Second), 32)
	if err != nil {
		s.Log.Warn("LED patrol query failed", "err", err)
		return
	}
	for _, d := range devices {
		s.applyLEDWindow(ctx, d, now)
	}
}

// applyLEDWindow compares one AP's confirmed LED state with the state its
// window implies right now and queues a correction when they differ. The
// minute patrol and the save handler share it, so a freshly saved window takes
// effect at once instead of waiting for the next tick.
func (s *Server) applyLEDWindow(ctx context.Context, d store.Device, now time.Time) {
	target, ok := ledWindowState(now, d.LEDOnAt, d.LEDOffAt)
	if !ok {
		s.Log.Warn("LED window has a bad clock", "mac", d.MAC, "on", d.LEDOnAt, "off", d.LEDOffAt)
		return
	}
	if d.LEDState == target {
		if err := s.Store.ResetLEDRepair(ctx, d.MAC); err != nil {
			s.Log.Warn("LED repair not reset", "mac", d.MAC, "err", err)
		}
		return
	}
	if d.LEDRetryAt > now.Unix() {
		return
	}

	pending, err := s.Store.HasFreshAction(ctx, d.MAC, proto.ActionLEDSet, now.Unix())
	if err != nil {
		s.Log.Warn("pending LED action check failed", "mac", d.MAC, "err", err)
		return
	}
	if pending {
		// A command that could still run is already on its way.
		return
	}

	a, err := s.Store.CreateAction(ctx, d.MAC, proto.ActionLEDSet, map[string]string{"state": target})
	if err != nil {
		s.Log.Warn("LED repair not queued", "mac", d.MAC, "err", err)
		return
	}
	attempt := d.LEDRetryCount + 1
	next := now.Add(ledRetryDelay(attempt))
	if err := s.Store.NoteLEDRepair(ctx, d.MAC, attempt, next.Unix()); err != nil {
		s.Log.Warn("LED repair not recorded", "mac", d.MAC, "err", err)
	}
	// Log the first attempt, the one that crosses the warning threshold and
	// then every tenth: a permanently broken AP stays visible without filling
	// the event log.
	if attempt == 1 || attempt == ledRepairWarnAfter || attempt%10 == 0 {
		kind := "led_repair"
		if attempt == ledRepairWarnAfter {
			kind = "led_repair_stuck"
		}
		s.Store.LogEvent(ctx, kind, d.MAC, fmt.Sprintf("attempt %d, expected %s, id %s", attempt, target, a.ID))
	}
	s.Log.Info("LED repair queued", "mac", d.MAC, "id", a.ID, "state", target, "attempt", attempt)
}

// reconcileLED applies a just-saved window to one device immediately. An
// offline AP is skipped: it will be corrected by the patrol once its heartbeat
// returns.
func (s *Server) reconcileLED(ctx context.Context, d store.Device) {
	if d.State != store.StateAdopted || d.LEDOnAt == "" || d.LEDOffAt == "" {
		return
	}
	now := s.now()
	if d.LastSeen < now.Unix()-int64(s.offlineAfter()/time.Second) {
		return
	}
	s.applyLEDWindow(ctx, d, now)
}

// ledDueTarget picks the state the schedule expects right now. With both clocks
// set the current window decides, so a device returning after a long outage
// converges on today's state in one command instead of replaying every missed
// edge. With a single clock only the due edge carries intent.
func ledDueTarget(d store.Device, onDue, offDue bool, now time.Time) (string, bool) {
	if d.LEDOnAt != "" && d.LEDOffAt != "" {
		return ledWindowState(now, d.LEDOnAt, d.LEDOffAt)
	}
	if d.LEDOnAt != "" && onDue {
		return "on", true
	}
	if d.LEDOffAt != "" && offDue {
		return "off", true
	}
	return "", false
}

// ledWindowState returns on when now falls inside the daily on→off window. An
// overnight schedule (on later than off) wraps past midnight.
func ledWindowState(now time.Time, onAt, offAt string) (string, bool) {
	onH, onM, err := parseClock(onAt)
	if err != nil {
		return "", false
	}
	offH, offM, err := parseClock(offAt)
	if err != nil {
		return "", false
	}
	nowMin := now.Hour()*60 + now.Minute()
	onMin, offMin := onH*60+onM, offH*60+offM
	on := nowMin >= onMin && nowMin < offMin
	if onMin > offMin {
		on = nowMin >= onMin || nowMin < offMin
	}
	if on {
		return "on", true
	}
	return "off", true
}

// advanceLEDClocks moves both configured edges to their next occurrence after
// now. Both are advanced together: replaying a stale pair of opposite edges is
// what leaves the final state dependent on command ordering.
func advanceLEDClocks(d store.Device, now time.Time) (onNext, offNext int64, ok bool) {
	onNext, offNext = d.LEDOnNextAt, d.LEDOffNextAt
	if d.LEDOnAt != "" {
		t, err := nextClockOccurrence(now, d.LEDOnAt)
		if err != nil {
			return 0, 0, false
		}
		onNext = t.Unix()
	}
	if d.LEDOffAt != "" {
		t, err := nextClockOccurrence(now, d.LEDOffAt)
		if err != nil {
			return 0, 0, false
		}
		offNext = t.Unix()
	}
	return onNext, offNext, true
}
