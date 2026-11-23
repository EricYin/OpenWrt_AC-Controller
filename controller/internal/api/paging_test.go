package api

import (
	"net/http/httptest"
	"testing"

	"github.com/naoki66/ac-controller/controller/internal/store"
)

func TestParsePageQuery(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/v1/clients?limit=2&offset=3&q=Lab", nil)
	p, err := parsePageQuery(r)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if p.Limit != 2 || p.Offset != 3 || p.Q != "lab" {
		t.Fatalf("parsed = %+v", p)
	}

	for _, raw := range []string{"limit=0", "limit=-1", "limit=5001", "limit=x", "offset=-1", "offset=x"} {
		if _, err := parsePageQuery(httptest.NewRequest("GET", "/api/v1/clients?"+raw, nil)); err == nil {
			t.Errorf("%s: expected an error", raw)
		}
	}

	empty, err := parsePageQuery(httptest.NewRequest("GET", "/api/v1/clients", nil))
	if err != nil || empty.limited() {
		t.Fatalf("unparameterised query should be unlimited: %+v, %v", empty, err)
	}
}

func TestPaginate(t *testing.T) {
	items := []int{1, 2, 3, 4, 5}
	if got := paginate(items, pageQuery{}); len(got) != 5 {
		t.Fatalf("unlimited = %v", got)
	}
	if got := paginate(items, pageQuery{Limit: 2, Offset: 1}); len(got) != 2 || got[0] != 2 || got[1] != 3 {
		t.Fatalf("page = %v", got)
	}
	if got := paginate(items, pageQuery{Limit: 2, Offset: 9}); len(got) != 0 {
		t.Fatalf("out of range = %v", got)
	}
}

func TestFilterClientsMatchesIdentityFields(t *testing.T) {
	list := []store.Client{
		{MAC: "8c:de:52:11:22:33", Alias: "客厅电视", IP: "10.0.10.50", SSID: "Home"},
		{MAC: "8c:de:52:44:55:66", Alias: "Laptop", IP: "10.0.10.51", SSID: "Lab"},
	}
	if got := filterClients(list, "laptop"); len(got) != 1 || got[0].MAC != list[1].MAC {
		t.Fatalf("alias filter = %+v", got)
	}
	if got := filterClients(list, "10.0.10.50"); len(got) != 1 || got[0].MAC != list[0].MAC {
		t.Fatalf("ip filter = %+v", got)
	}
	if got := filterClients(list, "8c:de:52"); len(got) != 2 {
		t.Fatalf("mac prefix filter = %+v", got)
	}
	if got := filterClients(list, "nomatch"); len(got) != 0 {
		t.Fatalf("no-match filter = %+v", got)
	}
}
