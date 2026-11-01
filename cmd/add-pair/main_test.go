package main

import (
	"net"
	"strings"
	"testing"
)

const testServerConfig = `[Interface]
Address = 10.77.0.1/24
PrivateKey = server-private
Jc = 5
H1 = 1

[Peer]
PublicKey = radio-public
AllowedIPs = 10.77.0.2/32, 192.168.66.0/24

[Peer]
PublicKey = module-public
AllowedIPs = 10.77.0.3/32, 192.168.55.0/24
`

func TestParseServerConfig(t *testing.T) {
	config, err := parseServerConfig(testServerConfig)
	if err != nil {
		t.Fatal(err)
	}
	if config.interfaceValues["Address"] != "10.77.0.1/24" {
		t.Fatalf("unexpected server address: %s", config.interfaceValues["Address"])
	}
	if len(config.peers) != 2 || len(config.peers[0].allowedIPs) != 2 {
		t.Fatalf("unexpected peer count: %+v", config.peers)
	}
}

func TestAllocateVPNAddresses(t *testing.T) {
	config, err := parseServerConfig(testServerConfig)
	if err != nil {
		t.Fatal(err)
	}
	_, radio, module, err := allocateVPNAddresses(config)
	if err != nil {
		t.Fatal(err)
	}
	if got := radio.String(); got != "10.77.0.4" {
		t.Fatalf("Radio address = %s, want 10.77.0.4", got)
	}
	if got := module.String(); got != "10.77.0.5" {
		t.Fatalf("Module address = %s, want 10.77.0.5", got)
	}
}

func TestRejectOverlappingAllowedIPs(t *testing.T) {
	configText := strings.Replace(testServerConfig, "192.168.55.0/24", "192.168.66.0/25", 1)
	if _, err := parseServerConfig(configText); err == nil {
		t.Fatal("expected overlapping AllowedIPs to fail")
	}
}

func TestParseIPv4Network(t *testing.T) {
	if _, err := parseIPv4Network("192.168.67.1/24", "Radio LAN"); err == nil {
		t.Fatal("expected host bits to be rejected")
	}
	if _, err := parseIPv4Network("2001:db8::/64", "Radio LAN"); err == nil {
		t.Fatal("expected IPv6 network to be rejected")
	}
}

func TestValidateEndpoint(t *testing.T) {
	for _, endpoint := range []string{"vpn.example.com:51820", "203.0.113.1:51820"} {
		if err := validateEndpoint(endpoint); err != nil {
			t.Errorf("%s rejected: %v", endpoint, err)
		}
	}
	for _, endpoint := range []string{"vpn.example.com:0", "bad host:51820", "vpn.example.com"} {
		if err := validateEndpoint(endpoint); err == nil {
			t.Errorf("expected %s to be rejected", endpoint)
		}
	}
}

func TestClientProfileIncludesPeerRouteAndParameters(t *testing.T) {
	_, vpn, _ := net.ParseCIDR("10.77.0.0/24")
	_, remote, _ := net.ParseCIDR("192.168.56.0/24")
	ip := net.ParseIP("10.77.0.4").To4()
	profile := makeClientConfig(ip, "client-private", "server-public", "vpn.example.com:51820", *vpn, *remote,
		map[string]string{"Jc": "5", "H1": "1"})
	for _, expected := range []string{
		"Address = 10.77.0.4/32",
		"AllowedIPs = 10.77.0.0/24, 192.168.56.0/24",
		"Jc = 5",
		"H1 = 1",
	} {
		if !strings.Contains(profile, expected) {
			t.Errorf("profile missing %q:\n%s", expected, profile)
		}
	}
}
