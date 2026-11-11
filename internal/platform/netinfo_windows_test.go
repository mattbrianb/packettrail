//go:build windows

package platform

import "testing"

func TestNetInfo(t *testing.T) {
	gw := DefaultGateway()
	dns := DNSServers()
	t.Logf("gateway %v, dns %v", gw, dns)
	if !gw.IsValid() {
		t.Fatal("no default gateway found")
	}
}
