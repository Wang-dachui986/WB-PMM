package main

import (
	"strings"
	"testing"
)

func TestDesktopListenMode(t *testing.T) {
	for _, tt := range []struct {
		listen     string
		lan, valid bool
	}{
		{"127.0.0.1:7863", false, true}, {"0.0.0.0:7863", true, true},
		{"0.0.0.0:65535", true, true}, {"127.0.0.1:1024", false, true}, {"127.0.0.1:1023", false, false},
		{"localhost:7863", false, false}, {":7863", false, false},
		{"192.168.1.1:7863", false, false}, {"[::]:7863", false, false},
		{"0.0.0.0:07863", false, false}, {"0.0.0.0:0", false, false},
		{"0.0.0.0:65536", false, false}, {"0.0.0.0:http", false, false},
	} {
		lan, err := desktopListenMode(tt.listen)
		if (err == nil) != tt.valid || lan != tt.lan {
			t.Errorf("%q: lan=%v err=%v", tt.listen, lan, err)
		}
	}
}

func TestReadDesktopHandshakeAccepts32ByteHexSecret(t *testing.T) {
	key := strings.Repeat("ab", desktopManagementKeyBytes)
	got, err := readDesktopHandshake(strings.NewReader(`{"management_key":"` + key + `"}` + "\n"))
	if err != nil {
		t.Fatalf("readDesktopHandshake() error = %v", err)
	}
	if got != key {
		t.Fatalf("readDesktopHandshake() = %q, want the validated key", got)
	}
}

func TestReadDesktopHandshakeRejectsMissingMalformedAndShortSecrets(t *testing.T) {
	tests := []string{
		"",
		`{"management_key":""}`,
		`{"management_key":"not-hex"}`,
		`{"management_key":"` + strings.Repeat("ab", desktopManagementKeyBytes-1) + `"}`,
	}
	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			if _, err := readDesktopHandshake(strings.NewReader(input)); err == nil {
				t.Fatal("readDesktopHandshake() unexpectedly accepted invalid input")
			}
		})
	}
}
