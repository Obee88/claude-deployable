// Package mcpx collects helpers for the bridge's MCP server: structured
// error shapes, repo-allowlist enforcement, and config-file loading.
//
// It deliberately stays small and SDK-adjacent rather than wrapping the
// SDK — cmd/bridge calls mcp.AddTool directly so tool handlers stay easy
// to read.
package mcpx

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Error codes used in the structured error shape.  Skills can branch on
// these without parsing free-form messages.
const (
	CodeOutsideAllowlist = "outside_allowlist"
	CodeInvalidArgs      = "invalid_args"
	CodeGitFailed        = "git_failed"
	CodeIOFailed         = "io_failed"
	CodeNotImplemented   = "not_implemented"
)

// Error is the structured error shape returned to MCP clients in tool
// results' StructuredContent / TextContent.  Callers should prefer
// ToErrorResult to wrap one into a CallToolResult.
//
// Error implements the standard error interface so handlers can return
// it directly.  The JSON wire shape is `{error, code}`.
type Error struct {
	Message string `json:"error"`
	Code    string `json:"code"`
}

// Error implements the error interface; returns the human-readable
// message field.
func (e Error) Error() string { return e.Message }

// New builds a structured Error.
func New(code, msg string) Error { return Error{Message: msg, Code: code} }

// Wrap attaches a code to a Go error.
func Wrap(code string, err error) Error { return Error{Message: err.Error(), Code: code} }

// ToErrorResult turns a structured Error into a CallToolResult marked
// IsError, with both a JSON TextContent payload (so skills get a
// machine-parseable body) and a short human message.
func ToErrorResult(e Error) *mcp.CallToolResult {
	body, _ := json.Marshal(e)
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{
			&mcp.TextContent{Text: string(body)},
		},
	}
}

// JSONResult marshals `v` as JSON into a single TextContent, suitable for
// returning the structured success body from a tool handler.
func JSONResult(v any) (*mcp.CallToolResult, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(body)}},
	}, nil
}

// Config holds the bridge's loaded runtime configuration.  See
// configs/bridge.env.example for the file format.
type Config struct {
	// Allowlist is the absolute, symlink-resolved set of repo paths the
	// bridge is permitted to operate on.  Any tool call whose `repo`
	// argument resolves outside this set is rejected with
	// CodeOutsideAllowlist.
	Allowlist []string

	// Identity is the author/committer identity used for commits.
	IdentityName  string
	IdentityEmail string

	// GHPAT is the fine-grained PAT used for git push and (later) the
	// GitHub REST API.  Logged only as "***" via Summary.
	GHPAT string
}

// Summary returns the no-secrets view of the config, suitable for the
// `bridge_ping` tool and stderr boot logs.
func (c Config) Summary() map[string]any {
	return map[string]any{
		"allowlist":      c.Allowlist,
		"identity_name":  c.IdentityName,
		"identity_email": c.IdentityEmail,
		"gh_pat_present": c.GHPAT != "",
	}
}

// LoadConfig reads a KEY=VALUE env file and returns a Config.  Path
// defaults to $HOME/.claude-deployable/.env, overridable via
// CLAUDE_DEPLOYABLE_ENV.  Missing file is fatal — the bridge cannot
// operate without an allowlist.
//
// The file should be mode 0600.  We log a warning to stderr if it isn't,
// but proceed; treating it as fatal would be unfriendly during initial
// setup.
func LoadConfig(path string) (Config, error) {
	if path == "" {
		if env := os.Getenv("CLAUDE_DEPLOYABLE_ENV"); env != "" {
			path = env
		} else {
			home, err := os.UserHomeDir()
			if err != nil {
				return Config{}, fmt.Errorf("locate home dir: %w", err)
			}
			path = filepath.Join(home, ".claude-deployable", ".env")
		}
	}

	f, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open bridge env %q: %w", path, err)
	}
	defer f.Close()

	if info, err := f.Stat(); err == nil {
		if mode := info.Mode().Perm(); mode&0o077 != 0 {
			fmt.Fprintf(os.Stderr, "warn: bridge env %q has mode %o; recommend 0600\n", path, mode)
		}
	}

	cfg := Config{}
	for line, err := range envLines(f) {
		if err != nil {
			return Config{}, fmt.Errorf("read bridge env: %w", err)
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		switch k {
		case "CLAUDE_DEPLOYABLE_ALLOWLIST":
			for _, p := range strings.Split(v, ",") {
				p = strings.TrimSpace(p)
				if p == "" {
					continue
				}
				abs, err := resolveRepo(p)
				if err != nil {
					return Config{}, fmt.Errorf("allowlist entry %q: %w", p, err)
				}
				cfg.Allowlist = append(cfg.Allowlist, abs)
			}
		case "GIT_AUTHOR_NAME":
			cfg.IdentityName = v
		case "GIT_AUTHOR_EMAIL":
			cfg.IdentityEmail = v
		case "GH_PAT":
			cfg.GHPAT = v
		}
	}
	if len(cfg.Allowlist) == 0 {
		return Config{}, errors.New("CLAUDE_DEPLOYABLE_ALLOWLIST is empty — refusing to start with no permitted repos")
	}
	if cfg.IdentityName == "" || cfg.IdentityEmail == "" {
		return Config{}, errors.New("GIT_AUTHOR_NAME and GIT_AUTHOR_EMAIL must both be set")
	}
	return cfg, nil
}

// ResolveAllowed validates that `repoArg` resolves (after symlink and
// relative-path resolution) to one of the allowlisted paths.  Returns
// the canonical absolute path on success, a structured Error on failure.
func (c Config) ResolveAllowed(repoArg string) (string, *Error) {
	abs, err := resolveRepo(repoArg)
	if err != nil {
		e := Wrap(CodeInvalidArgs, fmt.Errorf("resolve repo %q: %w", repoArg, err))
		return "", &e
	}
	for _, allowed := range c.Allowlist {
		if abs == allowed {
			return abs, nil
		}
	}
	e := New(CodeOutsideAllowlist, fmt.Sprintf("repo %q is not in the bridge allowlist", abs))
	return "", &e
}

func resolveRepo(p string) (string, error) {
	if p == "" {
		return "", errors.New("repo path is empty")
	}
	if !filepath.IsAbs(p) {
		abs, err := filepath.Abs(p)
		if err != nil {
			return "", err
		}
		p = abs
	}
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		// Treat a non-existent path as invalid rather than letting it
		// fall through to "not in allowlist" — the message is more
		// useful.
		return "", err
	}
	return resolved, nil
}

// envLines yields one logical KEY=VALUE line at a time, skipping blank
// lines and `#` comments.  Used by LoadConfig.  Iter form lets us
// surface read errors without a hand-rolled bufio loop.
func envLines(r io.Reader) func(yield func(string, error) bool) {
	return func(yield func(string, error) bool) {
		s := bufio.NewScanner(r)
		for s.Scan() {
			line := strings.TrimSpace(s.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if !yield(line, nil) {
				return
			}
		}
		if err := s.Err(); err != nil {
			yield("", err)
		}
	}
}

// LogJSON writes a structured JSON log line to stderr.  The bridge uses
// stderr because stdout is claimed by the MCP stdio transport.
func LogJSON(_ context.Context, fields map[string]any) {
	body, err := json.Marshal(fields)
	if err != nil {
		fmt.Fprintf(os.Stderr, `{"error":"log_marshal_failed","detail":%q}`+"\n", err.Error())
		return
	}
	fmt.Fprintln(os.Stderr, string(body))
}
