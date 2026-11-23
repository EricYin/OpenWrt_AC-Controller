package api

import (
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/naoki66/ac-controller/controller/internal/store"
	"github.com/naoki66/ac-controller/shared/version"
)

// handleMetrics serves a small Prometheus text exposition. It is deliberately
// unauthenticated: it carries aggregate counts only (no MACs, hostnames or
// PSKs), which is what a monitor on the same management LAN needs. Everything
// else stays behind the login.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	devices, err := s.Store.ListDevices(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.annotatePresence(devices)
	online, adopted := 0, 0
	for _, d := range devices {
		if d.Online {
			online++
		}
		if d.State == store.StateAdopted {
			adopted++
		}
	}

	clientsOnline, err := s.Store.CountClients(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	clientsTotal, err := s.Store.CountAllClients(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	profile := s.gate().Profile
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	fmt.Fprintf(w, "# HELP ac_build_info Controller build information.\n")
	fmt.Fprintf(w, "# TYPE ac_build_info gauge\n")
	fmt.Fprintf(w, "ac_build_info{version=\"%s\",commit=\"%s\",profile=\"%s\"} 1\n",
		metricLabel(version.Version), metricLabel(version.Commit), metricLabel(profile))
	fmt.Fprintf(w, "# HELP ac_uptime_seconds Seconds since the process started.\n")
	fmt.Fprintf(w, "# TYPE ac_uptime_seconds gauge\n")
	uptime := 0.0
	if !s.startedAt.IsZero() {
		uptime = time.Since(s.startedAt).Seconds()
	}
	fmt.Fprintf(w, "ac_uptime_seconds %.0f\n", uptime)
	fmt.Fprintf(w, "# HELP ac_devices_total APs that have ever checked in.\n")
	fmt.Fprintf(w, "# TYPE ac_devices_total gauge\n")
	fmt.Fprintf(w, "ac_devices_total %d\n", len(devices))
	fmt.Fprintf(w, "# HELP ac_devices_online APs seen within the offline threshold.\n")
	fmt.Fprintf(w, "# TYPE ac_devices_online gauge\n")
	fmt.Fprintf(w, "ac_devices_online %d\n", online)
	fmt.Fprintf(w, "# HELP ac_devices_adopted APs currently managed by this controller.\n")
	fmt.Fprintf(w, "# TYPE ac_devices_adopted gauge\n")
	fmt.Fprintf(w, "ac_devices_adopted %d\n", adopted)
	fmt.Fprintf(w, "# HELP ac_clients_online Stations currently associated.\n")
	fmt.Fprintf(w, "# TYPE ac_clients_online gauge\n")
	fmt.Fprintf(w, "ac_clients_online %d\n", clientsOnline)
	fmt.Fprintf(w, "# HELP ac_clients_total Stations ever recorded.\n")
	fmt.Fprintf(w, "# TYPE ac_clients_total gauge\n")
	fmt.Fprintf(w, "ac_clients_total %d\n", clientsTotal)
	fmt.Fprintf(w, "# HELP ac_http_requests_total HTTP requests served since start.\n")
	fmt.Fprintf(w, "# TYPE ac_http_requests_total counter\n")
	fmt.Fprintf(w, "ac_http_requests_total %d\n", s.httpTotal.Load())
	fmt.Fprintf(w, "# HELP ac_http_request_errors_total HTTP responses with status >= 400.\n")
	fmt.Fprintf(w, "# TYPE ac_http_request_errors_total counter\n")
	fmt.Fprintf(w, "ac_http_request_errors_total %d\n", s.httpErrors.Load())
	fmt.Fprintf(w, "# HELP ac_hello_total AP heartbeat requests.\n")
	fmt.Fprintf(w, "# TYPE ac_hello_total counter\n")
	fmt.Fprintf(w, "ac_hello_total %d\n", s.helloTotal.Load())
	fmt.Fprintf(w, "# HELP ac_hello_failed_total AP heartbeats refused with status >= 400.\n")
	fmt.Fprintf(w, "# TYPE ac_hello_failed_total counter\n")
	fmt.Fprintf(w, "ac_hello_failed_total %d\n", s.helloFailed.Load())
	fmt.Fprintf(w, "# HELP go_goroutines Goroutines currently running.\n")
	fmt.Fprintf(w, "# TYPE go_goroutines gauge\n")
	fmt.Fprintf(w, "go_goroutines %d\n", runtime.NumGoroutine())
	fmt.Fprintf(w, "# HELP go_memstats_alloc_bytes Bytes of allocated heap objects.\n")
	fmt.Fprintf(w, "# TYPE go_memstats_alloc_bytes gauge\n")
	fmt.Fprintf(w, "go_memstats_alloc_bytes %d\n", mem.Alloc)
}

// metricLabel escapes a value for a Prometheus label: backslash, double quote
// and newline are the only characters with special meaning.
func metricLabel(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
}
