package cli

import (
	"strings"
	"testing"
)

func TestCanonicalSupervisorCWD(t *testing.T) {
	got, err := canonicalSupervisorCWD(".")
	if err != nil {
		t.Fatalf("canonicalSupervisorCWD() error = %v", err)
	}
	if got == "." || !strings.HasPrefix(got, "/") {
		t.Fatalf("canonicalSupervisorCWD() = %q, want absolute clean path", got)
	}
}

func TestCodexHookIgnoresNonStopEvents(t *testing.T) {
	if err := runAxiCodexHook(strings.NewReader(`{"hook_event_name":"PostToolUse","session_id":"s","cwd":"/tmp"}`)); err != nil {
		t.Fatalf("runAxiCodexHook() error = %v", err)
	}
}

func TestCodexHookIgnoresMalformedPayload(t *testing.T) {
	if err := runAxiCodexHook(strings.NewReader(`not json`)); err != nil {
		t.Fatalf("runAxiCodexHook() error = %v", err)
	}
}

func TestSupervisorEnvReplacesExistingNMHome(t *testing.T) {
	t.Setenv("NM_HOME", "/old")
	env := supervisorEnv("/new")
	count := 0
	for _, entry := range env {
		if strings.HasPrefix(entry, "NM_HOME=") {
			count++
			if entry != "NM_HOME=/new" {
				t.Fatalf("NM_HOME entry = %q, want replacement", entry)
			}
		}
	}
	if count != 1 {
		t.Fatalf("NM_HOME count = %d, want 1", count)
	}
}
