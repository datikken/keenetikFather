package addpair

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"strings"
)

type serverConfig struct {
	interfaceValues map[string]string
	peers           []peerConfig
}

type peerConfig struct {
	publicKey  string
	allowedIPs []allowedNetwork
}

type allowedNetwork struct {
	prefix net.IPNet
}

func parseServerConfig(text string) (serverConfig, error) {
	var result serverConfig
	var currentSection string
	var currentValues map[string]string
	var interfaces []map[string]string
	var peers []map[string]string
	scanner := bufio.NewScanner(strings.NewReader(text))

	save := func() {
		switch currentSection {
		case "Interface":
			interfaces = append(interfaces, currentValues)
		case "Peer":
			peers = append(peers, currentValues)
		}
	}

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			save()
			currentSection = strings.TrimSpace(line[1 : len(line)-1])
			currentValues = make(map[string]string)
			continue
		}
		if currentValues == nil {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		if comment := strings.Index(value, "#"); comment >= 0 {
			value = value[:comment]
		}
		currentValues[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	if err := scanner.Err(); err != nil {
		return result, fmt.Errorf("read server config: %w", err)
	}
	save()

	if len(interfaces) != 1 {
		return result, errors.New("server.conf must contain exactly one [Interface] section")
	}
	if interfaces[0]["Address"] == "" || interfaces[0]["PrivateKey"] == "" {
		return result, errors.New("server [Interface] must include Address and PrivateKey")
	}
	result.interfaceValues = interfaces[0]
	for _, values := range peers {
		if values["PublicKey"] == "" || values["AllowedIPs"] == "" {
			return result, errors.New("each server [Peer] must include PublicKey and AllowedIPs")
		}
		peer := peerConfig{publicKey: values["PublicKey"]}
		for _, rawPrefix := range strings.Split(values["AllowedIPs"], ",") {
			_, network, err := net.ParseCIDR(strings.TrimSpace(rawPrefix))
			if err != nil {
				return result, fmt.Errorf("invalid AllowedIPs network %q: %w", rawPrefix, err)
			}
			if network.IP.To4() == nil {
				return result, errors.New("this project currently supports IPv4 peers only")
			}
			network.IP = network.IP.To4()
			peer.allowedIPs = append(peer.allowedIPs, allowedNetwork{prefix: *network})
		}
		result.peers = append(result.peers, peer)
	}
	if err := validateNoOverlaps(result.peers); err != nil {
		return result, err
	}
	return result, nil
}

func validateNoOverlaps(peers []peerConfig) error {
	var existing []net.IPNet
	for _, peer := range peers {
		for _, candidate := range peer.allowedIPs {
			for _, prior := range existing {
				if networksOverlap(candidate.prefix, prior) {
					return fmt.Errorf("overlapping AllowedIPs in server config: %s and %s", candidate.prefix, prior)
				}
			}
			existing = append(existing, candidate.prefix)
		}
	}
	return nil
}

func parseIPv4Network(value, label string) (net.IPNet, error) {
	ip, network, err := net.ParseCIDR(value)
	if err != nil || ip.To4() == nil || !ip.Equal(network.IP) {
		return net.IPNet{}, fmt.Errorf("%s must be a canonical IPv4 CIDR (for example 192.168.67.0/24)", label)
	}
	network.IP = network.IP.To4()
	return *network, nil
}

func networksOverlap(first, second net.IPNet) bool {
	return first.Contains(second.IP) || second.Contains(first.IP)
}

func vpnAddressPool(address string) (net.IPNet, []net.IP, error) {
	ip, network, err := net.ParseCIDR(address)
	if err != nil || ip.To4() == nil {
		return net.IPNet{}, nil, errors.New("server Address must be an IPv4 CIDR")
	}
	ip = ip.To4()
	network.IP = network.IP.To4()
	occupied := []net.IP{ip}
	return *network, occupied, nil
}

func (config serverConfig) allocateVPNAddresses() (net.IPNet, net.IP, net.IP, error) {
	network, occupied, err := vpnAddressPool(config.interfaceValues["Address"])
	if err != nil {
		return net.IPNet{}, nil, nil, err
	}
	for _, peer := range config.peers {
		for _, allowed := range peer.allowedIPs {
			if ones, bits := allowed.prefix.Mask.Size(); bits == 32 && ones == 32 && network.Contains(allowed.prefix.IP) {
				occupied = append(occupied, allowed.prefix.IP)
			}
		}
	}

	var addresses []net.IP
	candidate := nextIPv4(network.IP)
	for network.Contains(candidate) && len(addresses) < 2 {
		if !isBroadcast(network, candidate) && !containsIP(occupied, candidate) {
			addresses = append(addresses, append(net.IP(nil), candidate...))
		}
		candidate = nextIPv4(candidate)
	}
	if len(addresses) != 2 {
		return net.IPNet{}, nil, nil, fmt.Errorf("no two free VPN addresses remain in %s", &network)
	}
	return network, addresses[0], addresses[1], nil
}

func allocateVPNAddresses(config serverConfig) (net.IPNet, net.IP, net.IP, error) {
	return config.allocateVPNAddresses()
}

func nextIPv4(ip net.IP) net.IP {
	result := append(net.IP(nil), ip.To4()...)
	for index := len(result) - 1; index >= 0; index-- {
		result[index]++
		if result[index] != 0 {
			break
		}
	}
	return result
}

func isBroadcast(network net.IPNet, ip net.IP) bool {
	ones, bits := network.Mask.Size()
	if bits != 32 || ones >= 31 {
		return false
	}
	last := append(net.IP(nil), network.IP.To4()...)
	for index := range last {
		last[index] |= ^network.Mask[index]
	}
	return last.Equal(ip)
}

func containsIP(addresses []net.IP, candidate net.IP) bool {
	for _, address := range addresses {
		if address.Equal(candidate) {
			return true
		}
	}
	return false
}

func networkPrefixContains(parent, child net.IPNet) bool {
	ones, parentBits := parent.Mask.Size()
	childOnes, childBits := child.Mask.Size()
	return parentBits == childBits && ones <= childOnes && parent.Contains(child.IP)
}
