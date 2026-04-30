// Package dockerops shells out to the docker CLI to implement the
// /containers/* surface of cmd/vps-agent.
//
// We deliberately avoid the Docker Go SDK: the surface we need is
// trivially expressible as `docker inspect` / `docker logs` /
// `docker compose restart`, and the SDK would force a daemon-API
// version pin that complicates host upgrades. The agent runs as
// the deploy user, which is in the docker group (set up in M2),
// so the docker binary already authenticates against the local
// daemon.
//
// The Manager type holds the two pieces of policy this package
// cares about: the service-name allowlist (so a compromised
// auth-token can't restart arbitrary containers) and the compose
// project directory (where compose.yml lives, used by Restart).
// All public methods enforce the allowlist before invoking
// docker.
package dockerops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ErrNotAllowed is returned when a request names a service that
// isn't in the configured allowlist. Handlers should map this to
// 404 (not 403) so the response body doesn't reveal whether the
// service exists somewhere on the host.
var ErrNotAllowed = errors.New("service not in allowlist")

// ErrNotFound is returned when docker inspect reports the
// container does not exist (allowlisted but not yet created, or
// removed out of band). Handlers map this to 404 too.
var ErrNotFound = errors.New("container not found")

// Manager is the entry point for all container operations. It is
// goroutine-safe — the underlying docker invocations are
// independent processes.
type Manager struct {
	allowed    map[string]bool
	composeDir string
}

// NewManager builds a Manager from an allowlist of service names
// and the compose project directory. The allowlist is canonicalized
// (trimmed, empties dropped, deduped) so callers don't have to.
func NewManager(allowed []string, composeDir string) *Manager {
	a := make(map[string]bool, len(allowed))
	for _, n := range allowed {
		n = strings.TrimSpace(n)
		if n != "" {
			a[n] = true
		}
	}
	return &Manager{allowed: a, composeDir: composeDir}
}

// AllowedNames returns the allowlisted service names in sorted
// order. Used by List to know what to inspect; exported because
// tests benefit from being able to read the canonicalized list
// back.
func (m *Manager) AllowedNames() []string {
	out := make([]string, 0, len(m.allowed))
	for n := range m.allowed {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// IsAllowed reports whether name is in the allowlist. Exposed so
// HTTP handlers can short-circuit before constructing context for
// docker, but every Manager method also enforces this internally.
func (m *Manager) IsAllowed(name string) bool {
	return m.allowed[name]
}

// Container is the shape of an entry in the GET /containers
// response body. Aligned with PLAN.md's API surface table.
type Container struct {
	Name    string `json:"name"`
	Image   string `json:"image"`
	Status  string `json:"status"`
	UptimeS int64  `json:"uptime_s"`
	Health  string `json:"health"`
}

// Health is the shape returned by GET /containers/{name}/health.
// Status is one of "healthy", "unhealthy", "starting", "none"
// (no HEALTHCHECK declared), or "missing" (allowlisted but no
// container created yet). LastCheck and Details come from the
// most recent entry of State.Health.Log when present.
type Health struct {
	Status    string `json:"status"`
	LastCheck string `json:"last_check,omitempty"`
	Details   string `json:"details,omitempty"`
}

// Restart is the response body for POST /containers/{name}/restart.
// We don't return the docker compose stdout — it's noisy and not
// useful to the agent — just a confirmation and timestamp.
type Restart struct {
	Status      string `json:"status"`
	RestartedAt string `json:"restarted_at"`
}

// LogResult holds the output of GET /containers/{name}/logs along
// with truncation metadata. Body is plain text (docker logs is
// not guaranteed UTF-8, but in practice service output is); the
// HTTP handler writes Body verbatim.
type LogResult struct {
	Body          []byte
	Truncated     bool
	OriginalBytes int
}

// List returns metadata for every allowlisted service. Services
// that exist in the allowlist but have no container yet (e.g.
// hello on a fresh VPS before the first deploy) come back with
// Status="missing" rather than dropping out — the agent should
// see "I expected hello but it isn't there" as data.
func (m *Manager) List(ctx context.Context) ([]Container, error) {
	var out []Container
	for _, name := range m.AllowedNames() {
		raw, err := m.inspectRaw(ctx, name)
		if errors.Is(err, ErrNotFound) {
			out = append(out, Container{Name: name, Status: "missing", Health: "none"})
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, containerFromInspect(raw))
	}
	return out, nil
}

// Logs runs `docker logs --since <since> --tail <tail> <name>`,
// then optionally caps the output at maxBytes (last maxBytes
// retained, prefixed with a "[...truncated N bytes]" marker).
// Returning the LogResult lets the caller surface
// truncation in headers if it wants.
func (m *Manager) Logs(ctx context.Context, name, since string, tail, maxBytes int) (LogResult, error) {
	if !m.IsAllowed(name) {
		return LogResult{}, ErrNotAllowed
	}
	args := []string{"logs", "--timestamps"}
	if since != "" {
		args = append(args, "--since", since)
	}
	if tail > 0 {
		args = append(args, "--tail", strconv.Itoa(tail))
	}
	args = append(args, name)
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "No such container") {
			return LogResult{}, ErrNotFound
		}
		return LogResult{}, fmt.Errorf("docker logs %s: %w; output=%s", name, err, truncateForErr(out))
	}
	original := len(out)
	truncated := false
	if maxBytes > 0 && len(out) > maxBytes {
		out = out[len(out)-maxBytes:]
		out = append([]byte(fmt.Sprintf("[...truncated %d bytes]\n", original-len(out))), out...)
		truncated = true
	}
	return LogResult{Body: out, Truncated: truncated, OriginalBytes: original}, nil
}

// HealthOf returns the State.Health view of a container. Returns
// {Status: "missing"} for an allowlisted-but-uncreated container
// rather than ErrNotFound so /containers/{name}/health is
// consistent with /containers (both show "missing" rather than
// 404'ing).
func (m *Manager) HealthOf(ctx context.Context, name string) (Health, error) {
	if !m.IsAllowed(name) {
		return Health{}, ErrNotAllowed
	}
	raw, err := m.inspectRaw(ctx, name)
	if errors.Is(err, ErrNotFound) {
		return Health{Status: "missing"}, nil
	}
	if err != nil {
		return Health{}, err
	}
	if raw.State.Health == nil {
		return Health{Status: "none"}, nil
	}
	h := Health{Status: raw.State.Health.Status}
	if n := len(raw.State.Health.Log); n > 0 {
		last := raw.State.Health.Log[n-1]
		h.LastCheck = last.End
		h.Details = trimDetails(last.Output)
	}
	return h, nil
}

// Restart runs `docker compose -f <composeDir>/compose.yml
// restart <name>`. Compose's `restart` is preferred over `docker
// restart` because the compose project state (env_file, mount
// volumes, etc.) is the source of truth on this VPS — `docker
// restart` would skip compose's reconciliation.
func (m *Manager) Restart(ctx context.Context, name string) (Restart, error) {
	if !m.IsAllowed(name) {
		return Restart{}, ErrNotAllowed
	}
	if m.composeDir == "" {
		return Restart{}, errors.New("compose dir not configured")
	}
	composeFile := m.composeDir + "/compose.yml"
	cmd := exec.CommandContext(ctx, "docker", "compose", "-f", composeFile, "restart", name)
	cmd.Dir = m.composeDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return Restart{}, fmt.Errorf("docker compose restart %s: %w; output=%s", name, err, truncateForErr(out))
	}
	return Restart{
		Status:      "restarted",
		RestartedAt: time.Now().UTC().Format(time.RFC3339),
	}, nil
}

// inspectRaw runs docker inspect and parses the first array entry.
// Returns ErrNotFound for "No such object" so callers can map it
// to a 404 / "missing" without string-matching themselves.
func (m *Manager) inspectRaw(ctx context.Context, name string) (dockerInspect, error) {
	out, err := exec.CommandContext(ctx, "docker", "inspect", name).CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "No such object") || strings.Contains(string(out), "No such container") {
			return dockerInspect{}, ErrNotFound
		}
		return dockerInspect{}, fmt.Errorf("docker inspect %s: %w; output=%s", name, err, truncateForErr(out))
	}
	var arr []dockerInspect
	if err := json.Unmarshal(out, &arr); err != nil {
		return dockerInspect{}, fmt.Errorf("parse inspect output for %s: %w", name, err)
	}
	if len(arr) == 0 {
		return dockerInspect{}, ErrNotFound
	}
	return arr[0], nil
}

// containerFromInspect lifts the fields we care about out of the
// docker inspect blob. Pulled out of List so HealthOf can reuse
// the same logic if needed (it doesn't today, but a future
// /containers/{name} endpoint will).
func containerFromInspect(raw dockerInspect) Container {
	c := Container{
		Name:   strings.TrimPrefix(raw.Name, "/"),
		Image:  raw.Config.Image,
		Status: raw.State.Status,
		Health: "none",
	}
	if raw.State.Health != nil {
		c.Health = raw.State.Health.Status
	}
	if raw.State.Status == "running" {
		if t, err := time.Parse(time.RFC3339Nano, raw.State.StartedAt); err == nil {
			c.UptimeS = int64(time.Since(t).Seconds())
		}
	}
	return c
}

// dockerInspect mirrors only the fields cmd/vps-agent reads from
// `docker inspect`. The full payload is much larger; staying
// minimal keeps the parser robust to docker output changes.
//
// Note: dockerHealth is distinct from the public Health response
// type — the docker JSON has `{Status, Log[]}`, the public shape
// is `{status, last_check, details}`. Conflating them with one
// struct loses the Log array and inverts the casing.
type dockerInspect struct {
	Name  string `json:"Name"`
	State struct {
		Status    string        `json:"Status"`
		StartedAt string        `json:"StartedAt"`
		Health    *dockerHealth `json:"Health,omitempty"`
	} `json:"State"`
	Config struct {
		Image string `json:"Image"`
	} `json:"Config"`
}

type dockerHealth struct {
	Status string             `json:"Status"`
	Log    []dockerHealthLine `json:"Log"`
}

type dockerHealthLine struct {
	Output string `json:"Output"`
	End    string `json:"End"`
}

// trimDetails caps the health-check details surfaced via the API
// at 256 bytes. Docker's HEALTHCHECK output can be quite chatty
// (curl progress, stack traces); a head-cap is enough for the
// agent's purposes and saves agent context.
func trimDetails(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= 256 {
		return s
	}
	return s[:256] + "..."
}

// truncateForErr keeps error-message output short. docker errors
// often include stack-y text we don't want to log fully.
func truncateForErr(out []byte) string {
	const cap = 512
	if len(out) <= cap {
		return string(out)
	}
	return string(out[:cap]) + "...[truncated]"
}
