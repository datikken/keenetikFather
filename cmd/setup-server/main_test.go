package main

import (
	"bufio"
	"bytes"
	"net"
	"strings"
	"testing"
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

func bufioReader(value string) *bufio.Reader {
	return bufio.NewReader(strings.NewReader(value))
}
