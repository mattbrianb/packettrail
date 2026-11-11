package web

import "testing"

func TestParseTarget(t *testing.T) {
	cases := []struct {
		in, host, ip string
		port         int
		bad          bool
	}{
		{in: "8.8.8.8", ip: "8.8.8.8"},
		{in: "  1.1.1.1 ", ip: "1.1.1.1"},
		{in: "1.1.1.1:53", ip: "1.1.1.1", port: 53},
		{in: "2001:db8::1", ip: "2001:db8::1"},
		{in: "[2001:db8::1]", ip: "2001:db8::1"},
		{in: "[2001:db8::1]:443", ip: "2001:db8::1", port: 443},
		{in: "Example.COM", host: "example.com"},
		{in: "example.com.", host: "example.com"},
		{in: "example.com:8443", host: "example.com", port: 8443},
		{in: "https://www.example.com/path?q=1", host: "www.example.com"},
		{in: "http://10.0.0.1:8080/", ip: "10.0.0.1", port: 8080},
		{in: "ssh://admin@router.lan", host: "router.lan"},
		{in: "", bad: true},
		{in: "exa mple.com", bad: true},
		{in: "example.com:0", bad: true},
		{in: "example.com:99999", bad: true},
		{in: "-bad.example", bad: true},
	}
	for _, c := range cases {
		host, ip, port, err := parseTarget(c.in)
		if c.bad {
			if err == nil {
				t.Errorf("%q: want an error, got host %q ip %v", c.in, host, ip)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		gotIP := ""
		if ip.IsValid() {
			gotIP = ip.String()
		}
		if host != c.host || gotIP != c.ip || port != c.port {
			t.Errorf("%q: got host %q ip %q port %d, want %q %q %d", c.in, host, gotIP, port, c.host, c.ip, c.port)
		}
	}
}
