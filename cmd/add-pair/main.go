package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const imageName = "crsf2-amneziawg:local"

var (
	awgKeyPattern   = regexp.MustCompile(`^[A-Za-z0-9+/]{43}=$`)
	pairNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,47}$`)
)

var awgParameters = []string{
	"Jc", "Jmin", "Jmax", "S1", "S2", "S3", "S4", "H1", "H2", "H3", "H4",
}

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

type pairOptions struct {
	name      string
	radioLAN  net.IPNet
	moduleLAN net.IPNet
	endpoint  string
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

func allocateVPNAddresses(config serverConfig) (net.IPNet, net.IP, net.IP, error) {
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

func runCommand(ctx context.Context, input []byte, name string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, name, args...)
	root, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get project directory: %w", err)
	}
	command.Dir = root
	command.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", fmt.Errorf("command %s canceled: %w", name, ctxErr)
		}
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return "", fmt.Errorf("command %s failed: %s: %w", name, detail, err)
	}
	return strings.TrimSpace(stdout.String()), nil
}

func generateKey(ctx context.Context) (string, error) {
	key, err := runCommand(ctx, nil, "docker", "compose", "run", "--rm", "--no-deps", "-T", "--entrypoint", "awg", "amneziawg", "genkey")
	if err != nil {
		return "", err
	}
	if !awgKeyPattern.MatchString(key) {
		return "", errors.New("awg returned an invalid private key")
	}
	return key, nil
}

func publicKey(ctx context.Context, privateKey string) (string, error) {
	key, err := runCommand(ctx, []byte(privateKey+"\n"), "docker", "compose", "run", "--rm", "--no-deps", "-T", "--entrypoint", "awg", "amneziawg", "pubkey")
	if err != nil {
		return "", err
	}
	if !awgKeyPattern.MatchString(key) {
		return "", errors.New("awg returned an invalid public key")
	}
	return key, nil
}

func validateWithAWG(ctx context.Context, configPath string) error {
	absolute, err := filepath.Abs(configPath)
	if err != nil {
		return fmt.Errorf("resolve config path: %w", err)
	}
	_, err = runCommand(ctx, nil, "docker", "run", "--rm", "--network", "none",
		"--mount", fmt.Sprintf("type=bind,source=%s,target=/tmp/server.conf,readonly", absolute),
		"--entrypoint", "bash", imageName, "-lc", "awg-quick strip /tmp/server.conf")
	return err
}

func writeAtomic(path, contents string, mode os.FileMode) (returnErr error) {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	closed := false
	defer func() {
		if !closed {
			if err := temporary.Close(); err != nil {
				returnErr = errors.Join(returnErr, fmt.Errorf("close temporary file: %w", err))
			}
		}
		if err := os.Remove(temporaryPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			returnErr = errors.Join(returnErr, fmt.Errorf("remove temporary file: %w", err))
		}
	}()
	if err := temporary.Chmod(mode); err != nil {
		return fmt.Errorf("set temporary file permissions: %w", err)
	}
	if _, err := temporary.WriteString(contents); err != nil {
		return fmt.Errorf("write temporary file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		closed = true
		return fmt.Errorf("close temporary file: %w", err)
	}
	closed = true
	return os.Rename(temporaryPath, path)
}

func addPair(ctx context.Context, options pairOptions) (pairPath string, returnErr error) {
	root, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get project directory: %w", err)
	}
	configPath := filepath.Join(root, "config", "server.conf")
	pairsDirectory := filepath.Join(root, "config", "pairs")
	lockPath := filepath.Join(root, "config", ".add-pair.lock")
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("another add-pair operation is active, or stale lock exists: %s", lockPath)
		}
		return "", fmt.Errorf("create lock file: %w", err)
	}
	if _, err := fmt.Fprintf(lock, "pid=%d\n", os.Getpid()); err != nil {
		returnErr = fmt.Errorf("write lock file: %w", err)
	}
	if err := lock.Close(); err != nil {
		returnErr = errors.Join(returnErr, fmt.Errorf("close lock file: %w", err))
	}
	if returnErr != nil {
		if err := os.Remove(lockPath); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("remove lock file: %w", err))
		}
		return "", returnErr
	}
	defer func() {
		if err := os.Remove(lockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			returnErr = errors.Join(returnErr, fmt.Errorf("remove lock file: %w", err))
		}
	}()

	if !pairNamePattern.MatchString(options.name) {
		return "", errors.New("pair name must be 1-48 lowercase letters, digits, hyphens, or underscores")
	}
	if err := validateEndpoint(options.endpoint); err != nil {
		return "", err
	}
	radioLAN, err := parseIPv4Network(options.radioLAN.String(), "Radio LAN")
	if err != nil {
		return "", err
	}
	moduleLAN, err := parseIPv4Network(options.moduleLAN.String(), "Module LAN")
	if err != nil {
		return "", err
	}
	if networksOverlap(radioLAN, moduleLAN) {
		return "", errors.New("Radio LAN and Module LAN must not overlap")
	}

	serverTextBytes, err := os.ReadFile(configPath)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", configPath, err)
	}
	serverText := string(serverTextBytes)
	if strings.Contains(serverText, "CHANGE_ME") || strings.Contains(serverText, "REPLACE_WITH_") {
		return "", errors.New("replace all placeholders in config/server.conf before adding peers")
	}
	config, err := parseServerConfig(serverText)
	if err != nil {
		return "", err
	}
	vpnNetwork, radioIP, moduleIP, err := allocateVPNAddresses(config)
	if err != nil {
		return "", err
	}
	for _, candidate := range []net.IPNet{radioLAN, moduleLAN} {
		if networksOverlap(candidate, vpnNetwork) {
			return "", fmt.Errorf("LAN %s overlaps VPN subnet %s", &candidate, &vpnNetwork)
		}
		for _, peer := range config.peers {
			for _, existing := range peer.allowedIPs {
				if vpnNetwork.Contains(existing.prefix.IP) && networkPrefixContains(vpnNetwork, existing.prefix) {
					continue
				}
				if networksOverlap(candidate, existing.prefix) {
					return "", fmt.Errorf("LAN %s overlaps existing AllowedIPs network %s", &candidate, &existing.prefix)
				}
			}
		}
	}

	pairDirectory := filepath.Join(pairsDirectory, options.name)
	if _, err := os.Stat(pairDirectory); err == nil {
		return "", fmt.Errorf("pair already exists: %s", pairDirectory)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(pairsDirectory, 0700); err != nil {
		return "", fmt.Errorf("create pair directory: %w", err)
	}
	if err := os.Chmod(pairsDirectory, 0700); err != nil {
		return "", err
	}

	serverPublic, err := publicKey(ctx, config.interfaceValues["PrivateKey"])
	if err != nil {
		return "", err
	}
	radioPrivate, err := generateKey(ctx)
	if err != nil {
		return "", err
	}
	radioPublic, err := publicKey(ctx, radioPrivate)
	if err != nil {
		return "", err
	}
	modulePrivate, err := generateKey(ctx)
	if err != nil {
		return "", err
	}
	modulePublic, err := publicKey(ctx, modulePrivate)
	if err != nil {
		return "", err
	}

	parameters := make(map[string]string)
	for _, key := range awgParameters {
		if value, ok := config.interfaceValues[key]; ok {
			parameters[key] = value
		}
	}
	radioConfig := makeClientConfig(radioIP, radioPrivate, serverPublic, options.endpoint, vpnNetwork, moduleLAN, parameters)
	moduleConfig := makeClientConfig(moduleIP, modulePrivate, serverPublic, options.endpoint, vpnNetwork, radioLAN, parameters)
	updatedServer := appendPeers(serverText, options.name, radioPublic, radioIP, radioLAN, modulePublic, moduleIP, moduleLAN)

	serverTemporary, err := os.CreateTemp(filepath.Dir(configPath), ".server.conf-*")
	if err != nil {
		return "", err
	}
	serverTemporaryPath := serverTemporary.Name()
	serverTemporaryClosed := false
	defer func() {
		if !serverTemporaryClosed {
			if err := serverTemporary.Close(); err != nil {
				returnErr = errors.Join(returnErr, fmt.Errorf("close temporary server config: %w", err))
			}
		}
		if err := os.Remove(serverTemporaryPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			returnErr = errors.Join(returnErr, fmt.Errorf("remove temporary server config: %w", err))
		}
	}()
	if err := serverTemporary.Chmod(0600); err != nil {
		return "", err
	}
	if _, err := serverTemporary.WriteString(updatedServer); err != nil {
		return "", err
	}
	if err := serverTemporary.Sync(); err != nil {
		return "", err
	}
	if err := serverTemporary.Close(); err != nil {
		serverTemporaryClosed = true
		return "", err
	}
	serverTemporaryClosed = true
	if err := validateWithAWG(ctx, serverTemporaryPath); err != nil {
		return "", fmt.Errorf("validate updated server config: %w", err)
	}

	stagingDirectory, err := os.MkdirTemp(pairsDirectory, "."+options.name+"-*")
	if err != nil {
		return "", err
	}
	if err := os.Chmod(stagingDirectory, 0700); err != nil {
		return "", err
	}
	stagingCommitted := false
	defer func() {
		if !stagingCommitted {
			if err := os.RemoveAll(stagingDirectory); err != nil {
				returnErr = errors.Join(returnErr, fmt.Errorf("remove staged client profiles: %w", err))
			}
		}
	}()
	for filename, contents := range map[string]string{"radio.conf": radioConfig, "module.conf": moduleConfig} {
		if err := writeAtomic(filepath.Join(stagingDirectory, filename), contents, 0600); err != nil {
			return "", err
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := os.Rename(stagingDirectory, pairDirectory); err != nil {
		return "", fmt.Errorf("publish client profiles: %w", err)
	}
	stagingCommitted = true
	if err := os.Rename(serverTemporaryPath, configPath); err != nil {
		if removeErr := os.RemoveAll(pairDirectory); removeErr != nil {
			return "", errors.Join(fmt.Errorf("update server config: %w", err), fmt.Errorf("remove unpublished client profiles: %w", removeErr))
		}
		return "", fmt.Errorf("update server config: %w", err)
	}
	return pairDirectory, nil
}

func networkPrefixContains(parent, child net.IPNet) bool {
	ones, parentBits := parent.Mask.Size()
	childOnes, childBits := child.Mask.Size()
	return parentBits == childBits && ones <= childOnes && parent.Contains(child.IP)
}

func restartIfRunning(ctx context.Context) (bool, error) {
	containerID, err := runCommand(ctx, nil, "docker", "compose", "ps", "-q", "amneziawg")
	if err != nil {
		return false, err
	}
	if containerID == "" {
		return false, nil
	}
	if _, err := runCommand(ctx, nil, "docker", "compose", "restart", "amneziawg"); err != nil {
		return false, err
	}
	for range 60 {
		status, err := runCommand(ctx, nil, "docker", "inspect", "--format", "{{.State.Status}}|{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}", containerID)
		if err != nil {
			return false, err
		}
		if strings.HasSuffix(status, "|healthy") {
			return true, nil
		}
		if strings.HasPrefix(status, "exited|") || strings.HasPrefix(status, "dead|") {
			return false, fmt.Errorf("gateway restart failed: container status %s; check docker compose logs", status)
		}
		if strings.HasSuffix(status, "|unhealthy") {
			return false, errors.New("gateway failed its healthcheck; check docker compose logs")
		}
		if err := waitForNextPoll(ctx, time.Second); err != nil {
			return false, err
		}
	}
	return false, errors.New("timed out waiting for the restarted gateway to become healthy")
}

func waitForNextPoll(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func main() {
	os.Exit(run())
}

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if len(os.Args) != 5 {
		fmt.Fprintln(os.Stderr, "Usage: go run ./cmd/add-pair <name> <radio-lan-cidr> <module-lan-cidr> <vds-endpoint:port>")
		return 2
	}
	radioLAN, err := parseIPv4Network(os.Args[2], "Radio LAN")
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return 2
	}
	moduleLAN, err := parseIPv4Network(os.Args[3], "Module LAN")
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return 2
	}
	pairPath, err := addPair(ctx, pairOptions{
		name:      os.Args[1],
		radioLAN:  radioLAN,
		moduleLAN: moduleLAN,
		endpoint:  os.Args[4],
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return 1
	}
	fmt.Printf("Created client profiles: %s and %s\n",
		filepath.Join(pairPath, "radio.conf"), filepath.Join(pairPath, "module.conf"))
	fmt.Println("Each profile contains a private key; transfer it only to its matching gateway.")

	restarted, err := restartIfRunning(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Pair was added, but gateway update failed:", err)
		fmt.Fprintln(os.Stderr, "After resolving the error, apply the config with: docker compose up -d")
		return 1
	}
	if restarted {
		fmt.Println("Gateway restarted and is healthy with the new peers.")
	} else {
		fmt.Println("Gateway is not running. Apply the new config with: docker compose up -d")
	}
	return 0
}
