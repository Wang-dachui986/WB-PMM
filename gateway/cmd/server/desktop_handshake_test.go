package main

import (
	"strings"
	"testing"
)

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
