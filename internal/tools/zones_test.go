package tools

import (
	"strings"
	"testing"
)

// hopsOf builds a path from a compact description, one word per hop:
//
//	lan      a private address (10.x)
//	cgnat    a carrier-NAT address (100.64.x)
//	link     a link-local address
//	as64500  a public address in that network
//	pub      a public address with no known network
//	-        a hop that did not answer
func hopsOf(spec string) []Hop {
	var hops []Hop
	for i, w := range strings.Fields(spec) {
		h := Hop{TTL: i + 1}
		switch {
		case w == "-":
		case w == "lan":
			h.Addr, h.Scope = "10.0.0.1", "private"
		case w == "cgnat":
			h.Addr, h.Scope = "100.64.0.1", "cgnat"
		case w == "link":
			h.Addr, h.Scope = "169.254.0.1", "link-local"
		case w == "pub":
			h.Addr = "192.0.2.1"
		case strings.HasPrefix(w, "as"):
			h.Addr = "198.51.100.1"
			for _, c := range w[2:] {
				h.ASN = h.ASN*10 + uint32(c-'0')
			}
			h.ASOrg = "Example " + w[2:]
		default:
			panic("unknown hop " + w)
		}
		hops = append(hops, h)
	}
	return hops
}

// zonesOf prints the zones back, one word per hop: "lan", "provider",
// "in64500" for a provider hop inside that network, "." for no zone.
func zonesOf(hops []Hop) string {
	out := make([]string, len(hops))
	for i, h := range hops {
		switch {
		case h.Zone == ZoneProvider && h.InASN != 0:
			out[i] = "in" + strings.TrimPrefix(h.InOrg, "Example ")
		case h.Zone == "":
			out[i] = "."
		default:
			out[i] = h.Zone
		}
	}
	return strings.Join(out, " ")
}

func TestMarkZones(t *testing.T) {
	cases := []struct{ name, path, want string }{
		{"private hops between two hops of one network are inside it",
			"lan as64500 as64500 lan lan lan as64500",
			"lan . . in64500 in64500 in64500 ."},
		{"private hops between two different networks belong to a provider we cannot name",
			"lan as64500 lan lan as64501",
			"lan . provider provider ."},
		{"the local network can be several hops, with one that does not answer",
			"lan - lan as64500",
			"lan . lan ."},
		{"carrier NAT is already outside the local network",
			"lan cgnat lan as64500",
			"lan . provider ."},
		{"a path that never leaves private addresses is all local",
			"lan lan lan",
			"lan lan lan"},
		{"private hops at the end, with nothing public after them",
			"lan as64500 lan -",
			"lan . provider ."},
		{"hops that did not answer are skipped when looking for the next network",
			"lan as64500 lan - - as64500",
			"lan . in64500 . . ."},
		{"a public hop with no known network on one side names nobody",
			"lan pub lan as64500",
			"lan . provider ."},
		{"the same network again later does not reach back over another one",
			"lan as64500 as64501 lan as64500",
			"lan . . provider ."},
		{"link-local hops keep their own label",
			"link lan as64500 link",
			". lan . ."},
		{"no private hops at all",
			"as64500 as64501",
			". ."},
	}
	for _, c := range cases {
		hops := hopsOf(c.path)
		markZones(hops)
		if got := zonesOf(hops); got != c.want {
			t.Errorf("%s\n  path %s\n  got  %s\n  want %s", c.name, c.path, got, c.want)
		}
	}
}

// A hop inside a network carries that network's name, for the diagram's label.
func TestMarkZonesNamesTheNetwork(t *testing.T) {
	hops := hopsOf("lan as64500 lan as64500")
	markZones(hops)
	if h := hops[2]; h.InASN != 64500 || h.InOrg != "Example 64500" {
		t.Fatalf("hop 3: in AS%d %q, want AS64500 %q", h.InASN, h.InOrg, "Example 64500")
	}
	if h := hops[0]; h.InASN != 0 || h.InOrg != "" {
		t.Fatalf("the LAN hop was given a network: AS%d %q", h.InASN, h.InOrg)
	}
}
