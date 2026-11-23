package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/naoki66/ac-controller/controller/internal/store"
)

// maxPageSize bounds an explicit ?limit=. The default (no limit) stays
// unbounded for backward compatibility: the web UI polls the full snapshot
// because several pickers and counters need it.
const maxPageSize = 5000

// pageQuery is the optional paging/filtering applied to list endpoints. Zero
// Limit means "everything", which is what an unparameterised caller gets.
type pageQuery struct {
	Limit  int
	Offset int
	Q      string
}

func (p pageQuery) limited() bool { return p.Limit > 0 }

// parsePageQuery reads ?limit=&offset=&q=. A malformed value is a client error
// rather than a silently ignored parameter.
func parsePageQuery(r *http.Request) (pageQuery, error) {
	var p pageQuery
	q := r.URL.Query()

	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 || n > maxPageSize {
			return p, errors.New("limit must be an integer between 1 and 5000")
		}
		p.Limit = n
	}
	if raw := q.Get("offset"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			return p, errors.New("offset must be a non-negative integer")
		}
		p.Offset = n
	}
	p.Q = strings.ToLower(strings.TrimSpace(q.Get("q")))
	return p, nil
}

// paginate applies offset/limit to an already filtered slice.
func paginate[T any](items []T, p pageQuery) []T {
	if p.Offset > 0 {
		if p.Offset >= len(items) {
			return []T{}
		}
		items = items[p.Offset:]
	}
	if p.limited() && len(items) > p.Limit {
		items = items[:p.Limit]
	}
	return items
}

// filterDevices keeps rows whose human-visible identity fields contain q.
func filterDevices(list []store.Device, q string) []store.Device {
	if q == "" {
		return list
	}
	out := make([]store.Device, 0, len(list))
	for _, d := range list {
		if containsFold(q, d.MAC, d.Hostname, d.Model, d.IP, d.Firmware,
			d.TemplateID, d.GroupID, d.State) {
			out = append(out, d)
		}
	}
	return out
}

// filterClients keeps stations matching a name, MAC, address or SSID.
func filterClients(list []store.Client, q string) []store.Client {
	if q == "" {
		return list
	}
	out := make([]store.Client, 0, len(list))
	for _, c := range list {
		if containsFold(q, c.MAC, c.Alias, c.Note, c.IP, c.SSID, c.APMAC, c.Band) {
			out = append(out, c)
		}
	}
	return out
}

// containsFold reports whether q is a substring of any candidate, case folded.
func containsFold(q string, candidates ...string) bool {
	for _, c := range candidates {
		if c != "" && strings.Contains(strings.ToLower(c), q) {
			return true
		}
	}
	return false
}
