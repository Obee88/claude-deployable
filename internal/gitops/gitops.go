// Package gitops wraps the `git` CLI with a small, MCP-friendly surface:
// status, pull, branch, commit, push, reset, abort.
//
// All commits originate from the claude-agent identity via GIT_AUTHOR_*
// and GIT_COMMITTER_* environment variables (see PLAN.md decisions table).
// Pushes use a PAT injected into the remote URL for the call only — never
// written to .git/config.
package gitops

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// Identity is the author/committer identity used for commits made by the
// bridge.  Loaded from the bridge env file (GIT_AUTHOR_NAME, GIT_AUTHOR_EMAIL).
type Identity struct {
	Name  string
	Email string
}

// String renders the identity as `Name <email>` for log messages.
func (i Identity) String() string { return fmt.Sprintf("%s <%s>", i.Name, i.Email) }

// State is the high-level state of a working tree, used by skills to decide
// whether it's safe to proceed.  Anything other than Clean is a hand-off
// to the human (per AGENTS.md).
type State string

const (
	StateClean      State = "clean"
	StateDirty      State = "dirty"
	StateMerging    State = "merging"
	StateRebasing   State = "rebasing"
	StateDetached   State = "detached"
	StateCherryPick State = "cherry-picking"
)

// StatusResult mirrors the `git_status` MCP tool output.
type StatusResult struct {
	Branch     string   `json:"branch"`
	HeadSHA    string   `json:"head_sha"`
	DirtyFiles []string `json:"dirty_files"`
	Ahead      int      `json:"ahead"`
	Behind     int      `json:"behind"`
	State      State    `json:"state"`
}

// Status runs `git status --porcelain=v2 --branch` plus a couple of
// repo-state probes and returns the structured result.
func Status(ctx context.Context, repo string) (StatusResult, error) {
	out, err := runGit(ctx, repo, nil, "status", "--porcelain=v2", "--branch")
	if err != nil {
		return StatusResult{}, err
	}

	r := StatusResult{DirtyFiles: []string{}}
	for line := range strings.Lines(string(out)) {
		line = strings.TrimRight(line, "\n")
		switch {
		case strings.HasPrefix(line, "# branch.oid "):
			r.HeadSHA = strings.TrimPrefix(line, "# branch.oid ")
			if r.HeadSHA == "(initial)" {
				r.HeadSHA = ""
			}
		case strings.HasPrefix(line, "# branch.head "):
			r.Branch = strings.TrimPrefix(line, "# branch.head ")
			if r.Branch == "(detached)" {
				r.State = StateDetached
				r.Branch = ""
			}
		case strings.HasPrefix(line, "# branch.ab "):
			// "# branch.ab +N -M"
			parts := strings.Fields(strings.TrimPrefix(line, "# branch.ab "))
			if len(parts) == 2 {
				r.Ahead, _ = strconv.Atoi(strings.TrimPrefix(parts[0], "+"))
				r.Behind, _ = strconv.Atoi(strings.TrimPrefix(parts[1], "-"))
			}
		case strings.HasPrefix(line, "1 "), strings.HasPrefix(line, "2 "), strings.HasPrefix(line, "u "), strings.HasPrefix(line, "? "):
			r.DirtyFiles = append(r.DirtyFiles, parsePorcelainPath(line))
		}
	}

	// repo-state probes — these win over Dirty/Clean because they describe
	// in-progress operations the human needs to resolve before the agent
	// touches anything.
	gitDir, err := runGit(ctx, repo, nil, "rev-parse", "--git-dir")
	if err != nil {
		return r, err
	}
	gd := strings.TrimSpace(string(gitDir))
	if !strings.HasPrefix(gd, "/") {
		gd = repo + "/" + gd
	}
	switch {
	case fileExists(gd + "/MERGE_HEAD"):
		r.State = StateMerging
	case fileExists(gd+"/rebase-merge") || fileExists(gd+"/rebase-apply"):
		r.State = StateRebasing
	case fileExists(gd + "/CHERRY_PICK_HEAD"):
		r.State = StateCherryPick
	case r.State == StateDetached:
		// already set above
	case len(r.DirtyFiles) > 0:
		r.State = StateDirty
	default:
		r.State = StateClean
	}
	return r, nil
}

// parsePorcelainPath extracts the path from a porcelain v2 status line.
// Handles the four record kinds: 1 (changed), 2 (renamed/copied), u
// (unmerged), and ? (untracked). Renames have two paths separated by tab;
// we report the new path.
func parsePorcelainPath(line string) string {
	switch line[0] {
	case '?':
		return strings.TrimPrefix(line, "? ")
	case '1', 'u':
		// Eight space-separated fields then path.
		fields := strings.SplitN(line, " ", 9)
		if len(fields) >= 9 {
			return fields[8]
		}
	case '2':
		// 2 <XY> <sub> <mH> <mI> <mW> <hH> <hI> <X><score> <path>\t<origPath>
		fields := strings.SplitN(line, " ", 10)
		if len(fields) >= 10 {
			tab := strings.IndexByte(fields[9], '\t')
			if tab > 0 {
				return fields[9][:tab]
			}
			return fields[9]
		}
	}
	return ""
}

// PullResult is the shape returned by Pull.
type PullResult struct {
	UpdatedToSHA string `json:"updated_to_sha"`
}

// Pull runs `git pull --ff-only` (optionally targeting a specific branch).
// We deliberately refuse to merge — the agent should not synthesise merge
// commits silently; if a fast-forward is impossible the human resolves it.
func Pull(ctx context.Context, repo, branch string) (PullResult, error) {
	args := []string{"pull", "--ff-only"}
	if branch != "" {
		args = append(args, "origin", branch)
	}
	if _, err := runGit(ctx, repo, nil, args...); err != nil {
		return PullResult{}, err
	}
	sha, err := runGit(ctx, repo, nil, "rev-parse", "HEAD")
	if err != nil {
		return PullResult{}, err
	}
	return PullResult{UpdatedToSHA: strings.TrimSpace(string(sha))}, nil
}

// BranchResult is the shape returned by Branch.
type BranchResult struct {
	Branch string `json:"branch"`
	SHA    string `json:"sha"`
}

// Branch creates `name` (optionally from `fromRef`, defaulting to HEAD)
// and checks it out.  Refuses if `name` already exists.
func Branch(ctx context.Context, repo, name, fromRef string) (BranchResult, error) {
	if fromRef == "" {
		fromRef = "HEAD"
	}
	if _, err := runGit(ctx, repo, nil, "checkout", "-b", name, fromRef); err != nil {
		return BranchResult{}, err
	}
	sha, err := runGit(ctx, repo, nil, "rev-parse", "HEAD")
	if err != nil {
		return BranchResult{}, err
	}
	return BranchResult{Branch: name, SHA: strings.TrimSpace(string(sha))}, nil
}

// CommitResult is the shape returned by Commit.
type CommitResult struct {
	SHA string `json:"sha"`
}

// Commit stages `files` (or all working-tree changes if `files` is nil)
// and creates a commit using `id` for both author and committer.
//
// Implementation note: git's CLI reads GIT_AUTHOR_* and GIT_COMMITTER_*
// from the env, which is the documented mechanism for per-commit identity
// without touching the user's global config.
func Commit(ctx context.Context, repo string, id Identity, message string, files []string) (CommitResult, error) {
	if id.Name == "" || id.Email == "" {
		return CommitResult{}, fmt.Errorf("commit identity is not configured")
	}
	if strings.TrimSpace(message) == "" {
		return CommitResult{}, fmt.Errorf("commit message must not be empty")
	}

	addArgs := []string{"add", "--"}
	if len(files) == 0 {
		addArgs = []string{"add", "-A"}
	} else {
		addArgs = append(addArgs, files...)
	}
	if _, err := runGit(ctx, repo, nil, addArgs...); err != nil {
		return CommitResult{}, err
	}

	identityEnv := []string{
		"GIT_AUTHOR_NAME=" + id.Name,
		"GIT_AUTHOR_EMAIL=" + id.Email,
		"GIT_COMMITTER_NAME=" + id.Name,
		"GIT_COMMITTER_EMAIL=" + id.Email,
	}
	if _, err := runGit(ctx, repo, identityEnv, "commit", "-m", message); err != nil {
		return CommitResult{}, err
	}
	sha, err := runGit(ctx, repo, nil, "rev-parse", "HEAD")
	if err != nil {
		return CommitResult{}, err
	}
	return CommitResult{SHA: strings.TrimSpace(string(sha))}, nil
}

// PushResult is the shape returned by Push.
type PushResult struct {
	PushedSHA string `json:"pushed_sha"`
}

// Push pushes `branch` to origin.  If `pat` is non-empty, the PAT is
// injected into the remote URL for this call only via `git -c
// http.extraHeader=` style; we use a temporary remote URL push so the PAT
// is never written to .git/config.
//
// `setUpstream`, if true, adds `--set-upstream` so the local branch
// tracks origin/<branch> after the push.
func Push(ctx context.Context, repo, branch, pat string, setUpstream bool) (PushResult, error) {
	args := []string{"push"}
	if setUpstream {
		args = append(args, "--set-upstream")
	}

	if pat == "" {
		args = append(args, "origin", branch)
		if _, err := runGit(ctx, repo, nil, args...); err != nil {
			return PushResult{}, err
		}
	} else {
		// Resolve the configured origin URL, inject the PAT, and pass the
		// rewritten URL on the command line.  This never touches
		// .git/config.
		remoteOut, err := runGit(ctx, repo, nil, "remote", "get-url", "origin")
		if err != nil {
			return PushResult{}, err
		}
		injected, err := injectPAT(strings.TrimSpace(string(remoteOut)), pat)
		if err != nil {
			return PushResult{}, err
		}
		args = append(args, injected, branch)
		if _, err := runGit(ctx, repo, nil, args...); err != nil {
			// Ensure the PAT does not bleed into error output.  runGit
			// already captured stderr; filter it before bubbling up.
			return PushResult{}, redactPAT(err, pat)
		}
	}

	sha, err := runGit(ctx, repo, nil, "rev-parse", branch)
	if err != nil {
		return PushResult{}, err
	}
	return PushResult{PushedSHA: strings.TrimSpace(string(sha))}, nil
}

// injectPAT rewrites an https remote URL to embed an OAuth-style PAT in
// the userinfo (https://oauth2:<pat>@host/path).  Non-https URLs (ssh,
// scp-style git@host:path) are returned unchanged — the bridge is
// documented to use https origins.
func injectPAT(remote, pat string) (string, error) {
	if !strings.HasPrefix(remote, "https://") && !strings.HasPrefix(remote, "http://") {
		return remote, nil
	}
	u, err := url.Parse(remote)
	if err != nil {
		return "", fmt.Errorf("parse remote url: %w", err)
	}
	if u.Scheme != "https" {
		return remote, nil
	}
	u.User = url.UserPassword("oauth2", pat)
	return u.String(), nil
}

func redactPAT(err error, pat string) error {
	if pat == "" {
		return err
	}
	return fmt.Errorf("%s", strings.ReplaceAll(err.Error(), pat, "***"))
}

// ResetResult is the shape returned by Reset.
type ResetResult struct {
	HeadSHA string `json:"head_sha"`
}

// Reset runs `git reset --<mode> <ref>`.  Mode is one of soft|mixed|hard.
func Reset(ctx context.Context, repo, mode, ref string) (ResetResult, error) {
	switch mode {
	case "soft", "mixed", "hard":
	default:
		return ResetResult{}, fmt.Errorf("invalid reset mode %q (want soft|mixed|hard)", mode)
	}
	if ref == "" {
		return ResetResult{}, fmt.Errorf("reset ref must not be empty")
	}
	if _, err := runGit(ctx, repo, nil, "reset", "--"+mode, ref); err != nil {
		return ResetResult{}, err
	}
	sha, err := runGit(ctx, repo, nil, "rev-parse", "HEAD")
	if err != nil {
		return ResetResult{}, err
	}
	return ResetResult{HeadSHA: strings.TrimSpace(string(sha))}, nil
}

// AbortResult is the shape returned by Abort.
type AbortResult struct {
	StateBefore State `json:"state_before"`
	StateAfter  State `json:"state_after"`
}

// Abort runs the right `--abort` for the current state — merge, rebase,
// or cherry-pick — and re-reads status to confirm the result.
func Abort(ctx context.Context, repo string) (AbortResult, error) {
	before, err := Status(ctx, repo)
	if err != nil {
		return AbortResult{}, err
	}
	var sub string
	switch before.State {
	case StateMerging:
		sub = "merge"
	case StateRebasing:
		sub = "rebase"
	case StateCherryPick:
		sub = "cherry-pick"
	default:
		return AbortResult{StateBefore: before.State, StateAfter: before.State},
			fmt.Errorf("nothing to abort: state=%s", before.State)
	}
	if _, err := runGit(ctx, repo, nil, sub, "--abort"); err != nil {
		return AbortResult{StateBefore: before.State}, err
	}
	after, err := Status(ctx, repo)
	if err != nil {
		return AbortResult{StateBefore: before.State}, err
	}
	return AbortResult{StateBefore: before.State, StateAfter: after.State}, nil
}

// runGit invokes `git -C repo <args>` with the given extra environment
// (appended to os.Environ).  Stdout is returned on success; on failure
// the error wraps stderr verbatim so callers (and the MCP wrapper) can
// surface it.
func runGit(ctx context.Context, repo string, extraEnv []string, args ...string) ([]byte, error) {
	full := append([]string{"-C", repo}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
