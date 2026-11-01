package addpair

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var pairNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,47}$`)

type pairOptions struct {
	name      string
	radioLAN  net.IPNet
	moduleLAN net.IPNet
	endpoint  string
}

type pairManager struct {
	root string
	awg  awgClient
}

func newPairManager(root string) pairManager {
	return pairManager{
		root: root,
		awg:  newAWGClient(root),
	}
}

func (manager pairManager) add(ctx context.Context, options pairOptions) (pairPath string, returnErr error) {
	configPath := filepath.Join(manager.root, "config", "server.conf")
	pairsDirectory := filepath.Join(manager.root, "config", "pairs")
	lockPath := filepath.Join(manager.root, "config", ".add-pair.lock")
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
	vpnNetwork, radioIP, moduleIP, err := config.allocateVPNAddresses()
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

	serverPublic, err := manager.awg.publicKey(ctx, config.interfaceValues["PrivateKey"])
	if err != nil {
		return "", err
	}
	radioPrivate, err := manager.awg.generateKey(ctx)
	if err != nil {
		return "", err
	}
	radioPublic, err := manager.awg.publicKey(ctx, radioPrivate)
	if err != nil {
		return "", err
	}
	modulePrivate, err := manager.awg.generateKey(ctx)
	if err != nil {
		return "", err
	}
	modulePublic, err := manager.awg.publicKey(ctx, modulePrivate)
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
	if err := manager.awg.validateConfig(ctx, serverTemporaryPath); err != nil {
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
