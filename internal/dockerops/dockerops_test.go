package dockerops

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// The shell-out functions (List, Logs, Health, Restart) are
// integration-tested against the live VPS in M3.A.6 — running
// docker in CI is more friction than it's worth for the surface
// we have. The unit tests here focus on the policy logic
// (allowlist canonicalization, pre-shell-out guards, helpers).

func TestNewManager_CanonicalizesAllowlist(t *testing.T) {
	m := NewManager([]string{"hello", " hello ", "", "world", "  "}, "/home/deploy/compose")
	got := m.AllowedNames()
	want := []string{"hello", "world"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AllowedNames: got %v want %v", got, want)
	}
}

func TestIsAllowed(t *testing.T) {
	m := NewManager([]string{"hello"}, "/d")
	if !m.IsAllowed("hello") {
		t.Fatalf("hello should be allowed")
	}
	if m.IsAllowed("world") {
		t.Fatalf("world should not be allowed")
	}
	if m.IsAllowed("") {
		t.Fatalf("empty name should never be allowed")
	}
}

// All four mutating operations must guard the allowlist BEFORE
// touching exec.Command. Tests assert this without needing docker
// on the test host — ErrNotAllowed comes back synchronously, no
// shell-out attempted.
func TestLogs_AllowlistGuard(t *testing.T) {
	m := NewManager([]string{"hello"}, "/d")
	_, err := m.Logs(context.Background(), "intruder", "10m", 50, 1024)
	if !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("err: got %v want ErrNotAllowed", err)
	}
}

func TestHealthOf_AllowlistGuard(t *testing.T) {
	m := NewManager([]string{"hello"}, "/d")
	_, err := m.HealthOf(context.Background(), "intruder")
	if !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("err: got %v want ErrNotAllowed", err)
	}
}

func TestRestart_AllowlistGuard(t *testing.T) {
	m := NewManager([]string{"hello"}, "/d")
	_, err := m.Restart(context.Background(), "intruder")
	if !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("err: got %v want ErrNotAllowed", err)
	}
}

func TestRestart_RequiresComposeDir(t *testing.T) {
	m := NewManager([]string{"hello"}, "")
	_, err := m.Restart(context.Background(), "hello")
	if err == nil || !strings.Contains(err.Error(), "compose dir not configured") {
		t.Fatalf("err: got %v want compose-dir-not-configured", err)
	}
}

func TestTrimDetails_ShortPassesThrough(t *testing.T) {
	if got, want := trimDetails("  ok  "), "ok"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestTrimDetails_LongTruncates(t *testing.T) {
	in := strings.Repeat("a", 300)
	got := trimDetails(in)
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("expected ... suffix, got: %q", got)
	}
	// 256 + len("...") = 259
	if len(got) != 259 {
		t.Fatalf("len: got %d want 259", len(got))
	}
}

func TestContainerFromInspect_ParsesFields(t *testing.T) {
	raw := dockerInspect{
		Name: "/hello",
	}
	raw.State.Status = "running"
	raw.State.StartedAt = "2026-04-30T12:00:00.000000000Z"
	raw.State.Health = &dockerHealth{Status: "healthy"}
	raw.Config.Image = "ghcr.io/x/y/hello:main"

	c := containerFromInspect(raw)
	if c.Name != "hello" {
		t.Fatalf("name: got %q want %q (leading slash should be stripped)", c.Name, "hello")
	}
	if c.Image != "ghcr.io/x/y/hello:main" {
		t.Fatalf("image: got %q", c.Image)
	}
	if c.Status != "running" {
		t.Fatalf("status: got %q", c.Status)
	}
	if c.Health != "healthy" {
		t.Fatalf("health: got %q", c.Health)
	}
	if c.UptimeS <= 0 {
		t.Fatalf("uptime: got %d, expected > 0 for a started-in-past timestamp", c.UptimeS)
	}
}

func TestContainerFromInspect_NoHealthcheck(t *testing.T) {
	raw := dockerInspect{Name: "/x"}
	raw.State.Status = "running"
	raw.State.StartedAt = "2026-04-30T12:00:00Z"

	c := containerFromInspect(raw)
	if c.Health != "none" {
		t.Fatalf("health: got %q want %q", c.Health, "none")
	}
}
