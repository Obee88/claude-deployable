// Command bridge is the claude-deployable local bridge.
//
// It runs on the user's host as a Cowork MCP server (stdio transport),
// exposing the git / CI tools documented in PLAN.md.  The Cowork sandbox
// invokes these tools through the MCP protocol; the bridge carries out
// the filesystem and network work with the user's normal OS permissions.
//
// Configuration is loaded from $HOME/.claude-deployable/.env (or the
// path in $CLAUDE_DEPLOYABLE_ENV).  Stdout is reserved for the MCP
// stdio framing; all logging goes to stderr as JSON-per-line.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/davorobilinovic/claude-deployable/internal/gitops"
	"github.com/davorobilinovic/claude-deployable/internal/mcpx"
	"github.com/davorobilinovic/claude-deployable/internal/repomux"
)

// Version is overridable at build time via -ldflags "-X main.Version=...".
var Version = "dev"

func main() {
	cfg, err := mcpx.LoadConfig("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "bridge: config error: %v\n", err)
		os.Exit(2)
	}

	// Stderr boot log — captured by Cowork's plugin log on the laptop.
	bootLog := map[string]any{
		"event":   "bridge_start",
		"version": Version,
	}
	for k, v := range cfg.Summary() {
		bootLog[k] = v
	}
	mcpx.LogJSON(context.Background(), bootLog)

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "claude-deployable-bridge",
		Version: Version,
	}, nil)

	mu := &repomux.Mux{}
	registerTools(server, &cfg, mu)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil {
		log.Printf("server exited: %v", err)
		os.Exit(1)
	}
}

// --------------------------------------------------------------------
// Tool input / output structs.  Field tags drive both the JSON wire
// format and the MCP input-schema generation (`jsonschema` tags become
// the per-field description).
// --------------------------------------------------------------------

type repoArgs struct {
	Repo string `json:"repo" jsonschema:"absolute path of the target repo (must be in the bridge allowlist)"`
}

type pullArgs struct {
	Repo   string `json:"repo" jsonschema:"absolute path of the target repo (must be in the bridge allowlist)"`
	Branch string `json:"branch,omitempty" jsonschema:"branch to pull; defaults to the current branch's upstream"`
}

type branchArgs struct {
	Repo    string `json:"repo" jsonschema:"absolute path of the target repo (must be in the bridge allowlist)"`
	Name    string `json:"name" jsonschema:"name of the branch to create and check out"`
	FromRef string `json:"from_ref,omitempty" jsonschema:"ref to branch from; defaults to HEAD"`
}

type commitArgs struct {
	Repo    string   `json:"repo" jsonschema:"absolute path of the target repo (must be in the bridge allowlist)"`
	Message string   `json:"message" jsonschema:"commit message"`
	Files   []string `json:"files,omitempty" jsonschema:"specific files to stage; if omitted, all working-tree changes are staged"`
}

type pushArgs struct {
	Repo        string `json:"repo" jsonschema:"absolute path of the target repo (must be in the bridge allowlist)"`
	Branch      string `json:"branch" jsonschema:"branch to push to origin"`
	SetUpstream bool   `json:"set_upstream,omitempty" jsonschema:"if true, sets the local branch's upstream to origin/<branch>"`
}

type resetArgs struct {
	Repo string `json:"repo" jsonschema:"absolute path of the target repo (must be in the bridge allowlist)"`
	Mode string `json:"mode" jsonschema:"reset mode: soft, mixed, or hard"`
	Ref  string `json:"ref" jsonschema:"ref to reset to (e.g. HEAD~1, a SHA, or a branch name)"`
}

// registerTools wires every bridge tool onto the server.  Read-only
// tools skip the per-repo mutex; mutating tools take it.
func registerTools(server *mcp.Server, cfg *mcpx.Config, mu *repomux.Mux) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "bridge_ping",
		Description: "Returns the bridge's loaded configuration summary (allowlist, identity, gh_pat_present). Useful as an end-to-end check that the plugin is wired up correctly.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		body, _ := json.Marshal(map[string]any{
			"version": Version,
			"config":  cfg.Summary(),
		})
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(body)}},
		}, nil, nil
	})

	addReadTool(server, cfg, "git_status",
		"Return {branch, head_sha, dirty_files, ahead, behind, state} for the working tree. State is one of clean|dirty|merging|rebasing|cherry-picking|detached.",
		func(ctx context.Context, repo string, _ repoArgs) (any, error) {
			return gitops.Status(ctx, repo)
		})

	addWriteTool(server, cfg, mu, "git_pull",
		"Fast-forward pull from origin. Refuses to merge — non-FF state must be resolved by the human.",
		func(ctx context.Context, repo string, args pullArgs) (any, error) {
			return gitops.Pull(ctx, repo, args.Branch)
		})

	addWriteTool(server, cfg, mu, "git_branch",
		"Create and check out a new branch (optionally from a specific ref).",
		func(ctx context.Context, repo string, args branchArgs) (any, error) {
			if args.Name == "" {
				return nil, mcpx.New(mcpx.CodeInvalidArgs, "name must not be empty")
			}
			return gitops.Branch(ctx, repo, args.Name, args.FromRef)
		})

	addWriteTool(server, cfg, mu, "git_commit",
		"Stage changes and commit using the bridge's claude-agent identity. Stages `files` if given, otherwise all working-tree changes.",
		func(ctx context.Context, repo string, args commitArgs) (any, error) {
			id := gitops.Identity{Name: cfg.IdentityName, Email: cfg.IdentityEmail}
			return gitops.Commit(ctx, repo, id, args.Message, args.Files)
		})

	addWriteTool(server, cfg, mu, "git_push",
		"Push `branch` to origin. Uses the bridge's GH_PAT for HTTPS auth — the PAT is injected per-call into the remote URL and never written to .git/config.",
		func(ctx context.Context, repo string, args pushArgs) (any, error) {
			if args.Branch == "" {
				return nil, mcpx.New(mcpx.CodeInvalidArgs, "branch must not be empty")
			}
			return gitops.Push(ctx, repo, args.Branch, cfg.GHPAT, args.SetUpstream)
		})

	addWriteTool(server, cfg, mu, "git_reset",
		"git reset --<mode> <ref>. Mode is one of soft|mixed|hard.",
		func(ctx context.Context, repo string, args resetArgs) (any, error) {
			return gitops.Reset(ctx, repo, args.Mode, args.Ref)
		})

	addWriteTool(server, cfg, mu, "git_abort",
		"Run the right --abort for the current state (merge, rebase, or cherry-pick) and return {state_before, state_after}.",
		func(ctx context.Context, repo string, _ repoArgs) (any, error) {
			return gitops.Abort(ctx, repo)
		})
}

// addReadTool registers a tool with allowlist enforcement but no mutex.
//
// The handler signature is (ctx, repo, args) → (result, error).  The
// `repo` arg has already been validated against the allowlist.
func addReadTool[Args any](
	server *mcp.Server,
	cfg *mcpx.Config,
	name, description string,
	fn func(ctx context.Context, repo string, args Args) (any, error),
) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        name,
		Description: description,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args Args) (*mcp.CallToolResult, any, error) {
		repo := repoFromArgs(args)
		abs, errStruct := cfg.ResolveAllowed(repo)
		if errStruct != nil {
			logTool(ctx, name, repo, time.Now(), errStruct.Code, false)
			return mcpx.ToErrorResult(*errStruct), nil, nil
		}
		start := time.Now()
		out, err := fn(ctx, abs, args)
		if err != nil {
			structured := wrapToolError(err)
			logTool(ctx, name, abs, start, structured.Code, false)
			return mcpx.ToErrorResult(structured), nil, nil
		}
		logTool(ctx, name, abs, start, "ok", true)
		res, err := mcpx.JSONResult(out)
		return res, nil, err
	})
}

// addWriteTool is like addReadTool but acquires the per-repo mutex
// before invoking fn.  Used for any state-mutating tool (pull, branch,
// commit, push, reset, abort).
func addWriteTool[Args any](
	server *mcp.Server,
	cfg *mcpx.Config,
	mu *repomux.Mux,
	name, description string,
	fn func(ctx context.Context, repo string, args Args) (any, error),
) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        name,
		Description: description,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args Args) (*mcp.CallToolResult, any, error) {
		repo := repoFromArgs(args)
		abs, errStruct := cfg.ResolveAllowed(repo)
		if errStruct != nil {
			logTool(ctx, name, repo, time.Now(), errStruct.Code, false)
			return mcpx.ToErrorResult(*errStruct), nil, nil
		}
		unlock := mu.Lock(abs)
		defer unlock()

		start := time.Now()
		out, err := fn(ctx, abs, args)
		if err != nil {
			structured := wrapToolError(err)
			logTool(ctx, name, abs, start, structured.Code, false)
			return mcpx.ToErrorResult(structured), nil, nil
		}
		logTool(ctx, name, abs, start, "ok", true)
		res, err := mcpx.JSONResult(out)
		return res, nil, err
	})
}

// repoFromArgs pulls the `Repo` field out of any args struct.  We rely
// on every tool's argument struct embedding `repo` at the top level —
// json.Marshal/Unmarshal lets us avoid hand-rolling reflection.
func repoFromArgs(args any) string {
	body, err := json.Marshal(args)
	if err != nil {
		return ""
	}
	var r struct {
		Repo string `json:"repo"`
	}
	_ = json.Unmarshal(body, &r)
	return r.Repo
}

// wrapToolError converts a Go error into a structured Error.  Errors
// already typed as mcpx.Error are passed through with their code intact;
// anything else is reported as CodeGitFailed (the common case).
func wrapToolError(err error) mcpx.Error {
	if e, ok := err.(mcpx.Error); ok {
		return e
	}
	return mcpx.Wrap(mcpx.CodeGitFailed, err)
}

// logTool emits the structured per-call log line.
func logTool(ctx context.Context, tool, repo string, start time.Time, code string, ok bool) {
	mcpx.LogJSON(ctx, map[string]any{
		"event":      "tool_call",
		"tool":       tool,
		"repo":       repo,
		"duration_s": time.Since(start).Seconds(),
		"code":       code,
		"ok":         ok,
	})
}
