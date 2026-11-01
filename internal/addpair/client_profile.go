package addpair

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
)

var awgParameters = []string{
	"Jc", "Jmin", "Jmax", "S1", "S2", "S3", "S4", "H1", "H2", "H3", "H4",
}

func validateEndpoint(value string) error {
	host, portText, err := net.SplitHostPort(value)
	if err != nil {
		return errors.New("endpoint must be an IPv4 address or DNS name with port, e.g. vpn.example.com:51820")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("endpoint port must be between 1 and 65535")
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.To4() == nil {
			return errors.New("endpoint must use an IPv4 address or DNS name")
		}
		return nil
	}
	if len(host) > 253 {
		return errors.New("endpoint DNS name is too long")
	}
	for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return errors.New("endpoint must use a valid IPv4 address or DNS name")
		}
		for _, character := range label {
			if !(character >= 'a' && character <= 'z') &&
				!(character >= 'A' && character <= 'Z') &&
				!(character >= '0' && character <= '9') &&
				character != '-' {
				return errors.New("endpoint must use a valid IPv4 address or DNS name")
			}
		}
	}
	return nil
}

func makeClientConfig(address net.IP, privateKey, serverPublicKey, endpoint string, vpnNetwork, remoteLAN net.IPNet, parameters map[string]string) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "[Interface]\nAddress = %s/32\nPrivateKey = %s\nMTU = 1420\n", address, privateKey)
	for _, key := range awgParameters {
		if value, ok := parameters[key]; ok {
			fmt.Fprintf(&builder, "%s = %s\n", key, value)
		}
	}
	fmt.Fprintf(&builder, "\n[Peer]\nPublicKey = %s\nEndpoint = %s\nAllowedIPs = %s, %s\nPersistentKeepalive = 25\n",
		serverPublicKey, endpoint, &vpnNetwork, &remoteLAN)
	return builder.String()
}

func appendPeers(original, name, radioPublic string, radioIP net.IP, radioLAN net.IPNet, modulePublic string, moduleIP net.IP, moduleLAN net.IPNet) string {
	return strings.TrimRight(original, "\r\n") +
		fmt.Sprintf("\n\n# %s Radio\n[Peer]\nPublicKey = %s\nAllowedIPs = %s/32, %s\n"+
			"\n# %s Module\n[Peer]\nPublicKey = %s\nAllowedIPs = %s/32, %s\n",
			name, radioPublic, radioIP, &radioLAN, name, modulePublic, moduleIP, &moduleLAN)
}
