package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPromptSetupUsesDefaultLANs(t *testing.T) {
	input := strings.NewReader("8.8.8.8\n\n\n\n")
	var output bytes.Buffer
	values, err := promptSetup(input, &output)
	if err != nil {
		t.Fatal(err)
	}
	if values.endpoint != "8.8.8.8:51820" {
		t.Fatalf("endpoint = %q", values.endpoint)
	}
	if values.serverPort != defaultServerUDPPort {
		t.Fatalf("serverPort = %d, want %d", values.serverPort, defaultServerUDPPort)
	}
	if got := values.radioLAN.String(); got != defaultRadioLAN {
		t.Fatalf("Radio LAN = %s, want %s", got, defaultRadioLAN)
	}
	if got := values.moduleLAN.String(); got != defaultModuleLAN {
		t.Fatalf("Module LAN = %s, want %s", got, defaultModuleLAN)
	}
}

func TestPromptSetupAcceptsCustomLANs(t *testing.T) {
	input := strings.NewReader("8.8.8.8\n51821\n192.168.67.0/24\n192.168.56.0/24\n")
	values, err := promptSetup(input, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if values.endpoint != "8.8.8.8:51821" || values.serverPort != 51821 {
		t.Fatalf("endpoint/serverPort = %q/%d, want 8.8.8.8:51821/51821", values.endpoint, values.serverPort)
	}
	if got := values.radioLAN.String(); got != "192.168.67.0/24" {
		t.Fatalf("Radio LAN = %s", got)
	}
	if got := values.moduleLAN.String(); got != "192.168.56.0/24" {
		t.Fatalf("Module LAN = %s", got)
	}
}

func TestPromptSetupRejectsInvalidUDPPorts(t *testing.T) {
	input := strings.NewReader("8.8.8.8\nabc\n0\n65536\n51822\n\n\n")
	var output bytes.Buffer
	values, err := promptSetup(input, &output)
	if err != nil {
		t.Fatal(err)
	}
	if values.endpoint != "8.8.8.8:51822" {
		t.Fatalf("endpoint = %q, want 8.8.8.8:51822", values.endpoint)
	}
	if strings.Count(output.String(), "Введите номер UDP-порта от 1 до 65535.") != 3 {
		t.Fatal("expected invalid ports to produce validation messages")
	}
}

func TestPromptServerUDPPortAcceptsBoundaries(t *testing.T) {
	for _, testCase := range []struct {
		input string
		want  int
	}{
		{input: "1", want: 1},
		{input: "65535", want: 65535},
	} {
		t.Run(testCase.input, func(t *testing.T) {
			got, err := promptServerUDPPort(bufioReader(testCase.input+"\n"), &bytes.Buffer{}, defaultServerUDPPort)
			if err != nil {
				t.Fatal(err)
			}
			if got != testCase.want {
				t.Fatalf("port = %d, want %d", got, testCase.want)
			}
		})
	}
}

func TestPromptServerUDPPortRepromptsWhenPortIsInUse(t *testing.T) {
	input := bufioReader("\n51821\n")
	var output bytes.Buffer
	checks := 0
	port, err := promptServerUDPPortWithCheck(input, &output, 51820, func(port int) (bool, error) {
		checks++
		return port != 51820, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if port != 51821 {
		t.Fatalf("port = %d, want 51821", port)
	}
	if checks != 2 {
		t.Fatalf("availability checks = %d, want 2", checks)
	}
	if !strings.Contains(output.String(), "UDP-порт 51820 уже используется") {
		t.Fatalf("missing occupied-port message:\n%s", output.String())
	}
}

func TestIsUDPPortAvailableRejectsBoundPort(t *testing.T) {
	listener, err := net.ListenPacket("udp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	_, portText, err := net.SplitHostPort(listener.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	available, err := isUDPPortAvailable(port)
	if err != nil {
		t.Fatal(err)
	}
	if available {
		t.Fatalf("UDP port %d is reported available while already bound", port)
	}
}

func TestPromptSetupRejectsPrivatePublicEndpoint(t *testing.T) {
	input := strings.NewReader("192.168.1.10\n8.8.8.8\n\n\n\n")
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
	input := strings.NewReader("8.8.8.8\n\n192.168.66.0/24\n192.168.66.128/25\n")
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
	for _, value := range []string{"8.8.8.8", "1.1.1.1"} {
		if !isPublicIPv4(net.ParseIP(value).To4()) {
			t.Errorf("%s should be accepted by public IPv4 check", value)
		}
	}
	for _, value := range []string{
		"127.0.0.1", "192.168.1.1", "0.0.0.0",
		"100.64.0.1", "192.0.2.1", "198.18.0.1", "203.0.113.10", "240.0.0.1",
	} {
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
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
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

func TestArchiveExistingConfigPreservesSourceWhenBackupAlreadyExists(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config", "server.conf")
	if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("existing config"), 0600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 1, 20, 0, 0, 0, time.UTC)
	backupPath := filepath.Join(root, "config", backupDirName, now.Format("20060102-150405.000000000"))
	if err := os.MkdirAll(backupPath, 0700); err != nil {
		t.Fatal(err)
	}

	if _, err := archiveExistingConfig(root, now); err == nil {
		t.Fatal("expected a conflicting backup directory to fail")
	}
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "existing config" {
		t.Fatalf("source config = %q, want it to remain untouched", content)
	}
}

func TestWritePrivateFileDoesNotOverwriteExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.conf")
	if err := os.WriteFile(path, []byte("existing config"), 0600); err != nil {
		t.Fatal(err)
	}

	err := writePrivateFile(path, "replacement config")
	if err == nil {
		t.Fatal("expected an existing private file to be preserved")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "existing config" {
		t.Fatalf("file content = %q, want existing config", content)
	}
}

func TestWaitForNextPollReturnsWhenContextIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := waitForNextPoll(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("waitForNextPoll error = %v, want %v", err, context.Canceled)
	}
}

func TestInitialServerConfigUsesSelectedUDPPort(t *testing.T) {
	config := initialServerConfig("private-key", 51823)
	if !strings.Contains(config, "ListenPort = 51823") {
		t.Fatalf("server config missing selected listen port:\n%s", config)
	}
}

func bufioReader(value string) *bufio.Reader {
	return bufio.NewReader(strings.NewReader(value))
}
