package redact

import (
	"strings"
	"testing"
)

func TestSecrets(t *testing.T) {
	for in, want := range map[string]string{
		"my key is sk-ant-api03-abcdefghijklmnop ok":       "my key is [redacted] ok",
		"use ms_live_1234567890abcdef please":              "use [redacted] please",
		"Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.abc.d": "Authorization: [redacted]",
		"api_key=hunter2hunter2 and password: s3cr3tpass":  "api_key=[redacted] and password: [redacted]",
		"token ghp_abcdefghijklmnopqrstuvwxyz0123":         "token [redacted]",
		"AKIAABCDEFGHIJKLMNOP is aws":                      "[redacted] is aws",
		"Agent, are we on track?":                          "Agent, are we on track?",
		"risk-free sk-short ms_x":                          "risk-free sk-short ms_x",
	} {
		if got := Secrets(in); got != want {
			t.Errorf("Secrets(%q) = %q, want %q", in, got, want)
		}
	}
	if got := Secrets("Bearer abcdefgh12345678"); strings.Contains(got, "abcdefgh") {
		t.Fatal(got)
	}
}
