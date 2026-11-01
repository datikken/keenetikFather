package main

import (
	"bufio"
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPromptSetupUsesDefaultLANs(t *testing.T) {
	input := strings.NewReader("8.8.8.8\n\n\n")
	var output bytes.Buffer
	values, err := promptSetup(input, &output)
	if err != nil {
		t.Fatal(err)
	}
	if values.endpoint != "8.8.8.8:51820" {
		t.Fatalf("endpoint = %q", values.endpoint)
	}
	if got := values.radioLAN.String(); got != defaultRadioLAN {
		t.Fatalf("Radio LAN = %s, want %s", got, defaultRadioLAN)
	}
	if got := values.moduleLAN.String(); got != defaultModuleLAN {
		t.Fatalf("Module LAN = %s, want %s", got, defaultModuleLAN)
	}
}

func TestPromptSetupAcceptsCustomLANs(t *testing.T) {
	input := strings.NewReader("8.8.8.8\n192.168.67.0/24\n192.168.56.0/24\n")
	values, err := promptSetup(input, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if got := values.radioLAN.String(); got != "192.168.67.0/24" {
		t.Fatalf("Radio LAN = %s", got)
	}
	if got := values.moduleLAN.String(); got != "192.168.56.0/24" {
		t.Fatalf("Module LAN = %s", got)
	}
}

func TestPromptSetupRejectsPrivatePublicEndpoint(t *testing.T) {
	input := strings.NewReader("192.168.1.10\n8.8.8.8\n\n\n")
	var output bytes.Buffer
	_, err := promptSetup(input, &output)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Введите публичный IPv4-адрес") {
		t.Fatal("expected a validation message for the private address")
	}
}

func TestPromptSetupRejectsOverlappingLANs(t *testing.T) {
	input := strings.NewReader("8.8.8.8\n192.168.66.0/24\n192.168.66.128/25\n")
	if _, err := promptSetup(input, &bytes.Buffer{}); err == nil {
		t.Fatal("expected overlapping LANs to be rejected")
	}
}

func TestParseLANRejectsDeviceAddress(t *testing.T) {
	reader := bufioReader("192.168.66.73/24\n")
	_, err := promptLAN(reader, &bytes.Buffer{}, "LAN Radio", defaultRadioLAN)
	if err == nil {
		t.Fatal("expected EOF after invalid LAN to fail")
	}
}

func TestIsPublicIPv4(t *testing.T) {
	for _, value := range []string{"8.8.8.8", "203.0.113.10"} {
		if !isPublicIPv4(net.ParseIP(value).To4()) {
			t.Errorf("%s should be accepted by public IPv4 check", value)
		}
	}
	for _, value := range []string{"127.0.0.1", "192.168.1.1", "0.0.0.0"} {
		if isPublicIPv4(net.ParseIP(value).To4()) {
			t.Errorf("%s should be rejected", value)
		}
	}
}

func TestArchiveExistingConfigMovesServerAndAllPeerProfiles(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	pairsDir := filepath.Join(configDir, "pairs")
	if err := os.MkdirAll(filepath.Join(pairsDir, "old-pair"), 0700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		filepath.Join(configDir, "server.conf"):            "old server private key",
		filepath.Join(configDir, "radio-peer.conf"):        "old radio private key",
		filepath.Join(configDir, "module-peer.conf"):       "old module private key",
		filepath.Join(pairsDir, "old-pair", "radio.conf"):  "old pair radio private key",
		filepath.Join(pairsDir, "old-pair", "module.conf"): "old pair module private key",
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}

	now := time.Date(2026, 11, 1, 20, 0, 0, 0, time.UTC)
	backupPath, err := archiveExistingConfig(root, now)
	if err != nil {
		t.Fatal(err)
	}
	if backupPath == "" {
		t.Fatal("expected backup path")
	}
	for path, expected := range map[string]string{
		filepath.Join(backupPath, "server.conf"):                      files[filepath.Join(configDir, "server.conf")],
		filepath.Join(backupPath, "radio-peer.conf"):                  files[filepath.Join(configDir, "radio-peer.conf")],
		filepath.Join(backupPath, "module-peer.conf"):                 files[filepath.Join(configDir, "module-peer.conf")],
		filepath.Join(backupPath, "pairs", "old-pair", "radio.conf"):  files[filepath.Join(pairsDir, "old-pair", "radio.conf")],
		filepath.Join(backupPath, "pairs", "old-pair", "module.conf"): files[filepath.Join(pairsDir, "old-pair", "module.conf")],
	} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read backup %s: %v", path, err)
		}
		if string(content) != expected {
			t.Errorf("backup %s = %q, want %q", path, content, expected)
		}
	}
	for _, path := range []string{
		filepath.Join(configDir, "server.conf"),
		filepath.Join(configDir, "radio-peer.conf"),
		filepath.Join(configDir, "module-peer.conf"),
		pairsDir,
	} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Errorf("old config path %s still exists", path)
		}
	}
}

func TestArchiveExistingConfigDoesNothingWhenNoConfigExists(t *testing.T) {
	backupPath, err := archiveExistingConfig(t.TempDir(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if backupPath != "" {
		t.Fatalf("backup path = %q, want empty", backupPath)
	}
}

func bufioReader(value string) *bufio.Reader {
	return bufio.NewReader(strings.NewReader(value))
}
