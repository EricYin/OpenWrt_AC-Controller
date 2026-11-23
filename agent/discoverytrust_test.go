package agent

import (
	"net"
	"testing"
)

// A controller can never legitimately advertise these address classes; an offer
// that carries one is either misconfigured or spoofed.
func TestRoutableUnicast(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"10.10.30.5", true},
		{"192.168.1.1", true},
		{"fd00::1", true},
		{"127.0.0.1", false},
		{"0.0.0.0", false},
		{"169.254.1.1", false},
		{"224.0.0.1", false},
		{"::1", false},
		{"fe80::1", false},
	}
	for _, c := range cases {
		ip := net.ParseIP(c.host)
		if ip == nil {
			t.Fatalf("test address %q did not parse", c.host)
		}
		if got := routableUnicast(ip); got != c.want {
			t.Errorf("routableUnicast(%s) = %v, want %v", c.host, got, c.want)
		}
	}
}

// The normal path must not require a network round trip: an offer whose address
// equals the datagram source is trusted immediately.
func TestOfferHostTrustedSameSource(t *testing.T) {
	peer := &net.UDPAddr{IP: net.ParseIP("10.10.30.5")}
	if !offerHostTrusted("10.10.30.5:8090", "10.10.30.5", peer, "ac-1", "key") {
		t.Fatal("same-source offer was rejected")
	}
	if offerHostTrusted("10.10.30.5:8090", "not-an-ip", peer, "ac-1", "key") {
		t.Fatal("malformed address was accepted")
	}
	if offerHostTrusted("127.0.0.1:8090", "127.0.0.1", &net.UDPAddr{IP: net.ParseIP("10.10.30.5")}, "ac-1", "key") {
		t.Fatal("loopback offer was accepted")
	}
}
