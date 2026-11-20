// Per-device address allocation. A template is shared by the whole fleet, so
// its static interface addresses are a fleet-wide collision waiting to happen:
// two APs adopting the same template without a per-unit override both render
// the same management IP. This file owns the two guards against that —
// auto-staggering a template address that another unit already took, and
// refusing an operator-written override that collides — so the duplicate
// address is prevented at render time instead of discovered at 2 a.m.
package provision

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/naoki66/ac-controller/controller/internal/store"
	"github.com/naoki66/ac-controller/shared/model"
)

// usedAddresses maps every layer-3 address the controller has effectively
// handed out (bare IPv4, canonical form) to the device that owns it. The
// current device is excluded: its own previous allocation must not reserve
// against itself, or a re-push could never keep its address.
//
// Two sources count as "taken":
//   - a per-unit override (network_params.ipaddr), whatever the device state —
//     Release keeps the override precisely because it describes the unit, so a
//     pending unit's address is still claimed;
//   - for an adopted unit on a network-managing template, the template's own
//     static addresses for every interface the unit did not override — that is
//     the address the next unmodified adopt would otherwise receive too.
func (s *Service) usedAddresses(ctx context.Context, excludeMAC string) (map[string]string, error) {
	devices, err := s.Store.ListDevices(ctx)
	if err != nil {
		return nil, fmt.Errorf("provision: list devices for address allocation: %w", err)
	}

	used := make(map[string]string)
	for _, d := range devices {
		if d.MAC == excludeMAC {
			continue
		}

		var ov model.DeviceNetwork
		if d.NetworkParams != "" {
			if json.Unmarshal([]byte(d.NetworkParams), &ov) == nil {
				for _, addrs := range ov.IPAddr {
					for _, a := range addrs {
						if bare := addressOnly(a); bare != "" && !strings.Contains(bare, ":") {
							used[bare] = d.MAC
						}
					}
				}
			}
		}

		// Template addresses only matter while the unit actually renders them:
		// a device adopted onto a network-managing template without an
		// override for an interface is running that interface's address now.
		if d.State != store.StateAdopted || d.TemplateID == "" {
			continue
		}
		tpl, err := s.Store.GetTemplate(ctx, d.TemplateID)
		if err != nil || !tpl.ManagesNetwork() {
			continue
		}
		for _, iface := range tpl.WithDeviceNetwork(ov).Bridge.Interfaces {
			if iface.Proto != "static" {
				continue
			}
			if _, overridden := ov.IPAddr[iface.Name]; overridden {
				continue // already counted above
			}
			for _, a := range iface.IPAddr {
				if bare := addressOnly(a); bare != "" && !strings.Contains(bare, ":") {
					used[bare] = d.MAC
				}
			}
		}
	}
	return used, nil
}

// parseAddrNet turns a template address into the pieces allocation needs: the
// IP, its mask (from a CIDR suffix or the interface netmask, /32 when neither
// is given) and whether the rendered form should carry the "/n" suffix. The
// suffix is preserved because the rendered UCI list keeps the input style.
func parseAddrNet(addr, netmask string) (net.IP, net.IPMask, bool, error) {
	if i := strings.IndexByte(addr, '/'); i >= 0 {
		ip := net.ParseIP(strings.TrimSpace(addr[:i]))
		if ip == nil || ip.To4() == nil {
			return nil, nil, false, fmt.Errorf("%q is not an IPv4 address", addr)
		}
		n, err := strconv.Atoi(addr[i+1:])
		if err != nil || n < 0 || n > 32 {
			return nil, nil, false, fmt.Errorf("%q has no valid IPv4 prefix length", addr)
		}
		return ip.To4(), net.CIDRMask(n, 32), true, nil
	}

	ip := net.ParseIP(strings.TrimSpace(addr))
	if ip == nil || ip.To4() == nil {
		return nil, nil, false, fmt.Errorf("%q is not an IPv4 address", addr)
	}
	if netmask != "" {
		m := net.ParseIP(netmask)
		if m == nil || m.To4() == nil {
			return nil, nil, false, fmt.Errorf("netmask %q is not a dotted quad", netmask)
		}
		mask := net.IPMask(m.To4())
		// Size() reports -1 ones for a non-contiguous mask and 0 for /0;
		// neither yields a usable host range, and a contiguous /0 site does
		// not exist in practice.
		if ones, bits := mask.Size(); bits != 32 || ones <= 0 {
			return nil, nil, false, fmt.Errorf("netmask %q is not a usable IPv4 mask", netmask)
		}
		return ip.To4(), mask, false, nil
	}
	return ip.To4(), net.CIDRMask(32, 32), false, nil
}

// nextFree returns the free host address closest to base within its subnet:
// base itself when free, otherwise base+1, base+2 … up to the last host, then
// wrapping to the lowest host. Deterministic, so preflight and the actual
// generate see the same address for the same store state.
func nextFree(base net.IP, mask net.IPMask, used map[string]string) (net.IP, error) {
	free := func(ip net.IP) bool { _, taken := used[ip.String()]; return !taken }
	if len(base) != 4 || len(mask) != 4 {
		return nil, fmt.Errorf("only IPv4 addresses can be auto-assigned")
	}
	ones, bits := mask.Size()
	if bits != 32 {
		return nil, fmt.Errorf("only IPv4 addresses can be auto-assigned")
	}

	ip := binary.BigEndian.Uint32(base)
	m := binary.BigEndian.Uint32(mask)
	// broadcast = network with every host bit set: ip | (complement of mask).
	network, broadcast := ip&m, ip|^m

	// /31 and /32 have no room beyond the base address itself.
	if broadcast-network < 2 {
		if free(base) {
			return base, nil
		}
		return nil, fmt.Errorf("subnet /%d has no spare host address", ones)
	}

	first, last := network+1, broadcast-1
	hostSpace := int64(last - first + 1)
	offset := int64(ip) - int64(first)
	if offset < 0 || offset >= hostSpace {
		offset = 0 // base sits on the network or broadcast address
	}
	for i := int64(0); i < hostSpace; i++ {
		cand := make(net.IP, 4)
		binary.BigEndian.PutUint32(cand, first+uint32((offset+i)%hostSpace))
		if free(cand) {
			return cand, nil
		}
	}
	return nil, fmt.Errorf("subnet /%d has no free host address left", ones)
}
