// Package forge wraps the GitHub CLI, so nm shells out to `gh` in exactly one
// place, the way it shells out to git in exactly one place.
package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Timeout bounds any single gh invocation.
const Timeout = 60 * time.Second

// ErrNotAuthenticated means gh has no GitHub login to work with.
var ErrNotAuthenticated = errors.New("gh is not logged in to GitHub")

// ErrNotInstalled means the gh CLI is not on PATH.
var ErrNotInstalled = errors.New("the GitHub CLI (gh) is not installed")

// PR is a pull request nm created or found.
type PR struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
	State  string `json:"state"`
}

// Client runs the gh CLI.
type Client struct {
	Bin string // defaults to "gh"
}

func (c Client) bin() string {
	if c.Bin == "" {
		return "gh"
	}
	return c.Bin
}

func (c Client) run(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, c.bin(), args...)
	cmd.Dir = dir
	// Never stop for an interactive prompt: nm decides what to ask, not gh.
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1")

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		return strings.TrimSpace(stdout.String()), fmt.Errorf("gh %s: %s", strings.Join(args[:min(2, len(args))], " "), msg)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// CheckAuth verifies gh is installed and logged in, so a multi-repo run fails
// before it pushes anything rather than halfway through.
func (c Client) CheckAuth() error {
	if _, err := exec.LookPath(c.bin()); err != nil {
		return ErrNotInstalled
	}
	if _, err := c.run("", "auth", "status"); err != nil {
		return ErrNotAuthenticated
	}
	return nil
}

// Find returns the open pull request for a branch, or nil when there is none.
func (c Client) Find(dir, branch string) (*PR, error) {
	out, err := c.run(dir, "pr", "list", "--head", branch, "--state", "open",
		"--json", "number,url,state", "--limit", "1")
	if err != nil {
		return nil, err
	}
	return ParsePRList(out)
}

// ParsePRList decodes `gh pr list --json number,url,state` output.
func ParsePRList(out string) (*PR, error) {
	out = strings.TrimSpace(out)
	if out == "" || out == "[]" {
		return nil, nil
	}
	var prs []PR
	if err := json.Unmarshal([]byte(out), &prs); err != nil {
		return nil, fmt.Errorf("reading the pull request list: %w", err)
	}
	if len(prs) == 0 {
		return nil, nil
	}
	return &prs[0], nil
}

// CreateOptions describes a pull request to open.
type CreateOptions struct {
	Dir      string
	Base     string
	Head     string
	Title    string
	BodyFile string // a file, so a long body never hits argument limits
	Draft    bool
}

// Create opens a pull request and returns it.
func (c Client) Create(opts CreateOptions) (*PR, error) {
	args := []string{
		"pr", "create",
		"--base", opts.Base,
		// --head keeps gh from offering to push or fork on our behalf.
		"--head", opts.Head,
		"--title", opts.Title,
		"--body-file", opts.BodyFile,
	}
	if opts.Draft {
		args = append(args, "--draft")
	}
	if _, err := c.run(opts.Dir, args...); err != nil {
		return nil, err
	}
	// gh prints the URL, but reading it back gives the number too.
	return c.Find(opts.Dir, opts.Head)
}

// EditBody replaces the body of an existing pull request.
func (c Client) EditBody(dir, branch, bodyFile string) error {
	_, err := c.run(dir, "pr", "edit", branch, "--body-file", bodyFile)
	return err
}
