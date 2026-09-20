package task

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nitinshyamk/nm/internal/forge"
	"github.com/nitinshyamk/nm/internal/gitx"
)

// fakeForge stands in for the GitHub CLI: it records what it was asked to do
// and hands back plausible pull requests.
type fakeForge struct {
	authErr error
	// Keyed by directory and branch, because every repo in a task shares one
	// branch name while real gh scopes its search to one repository.
	existing map[string]*forge.PR
	created  []forge.CreateOptions
	bodies   map[string]string // repo dir -> body as last written
	edits    int
	next     int
	failOn   string // repo directory suffix that should fail to create
}

func newFakeForge() *fakeForge {
	return &fakeForge{existing: map[string]*forge.PR{}, bodies: map[string]string{}}
}

func (f *fakeForge) CheckAuth() error { return f.authErr }

func (f *fakeForge) Find(dir, branch string) (*forge.PR, error) {
	return f.existing[dir+"\x00"+branch], nil
}

func (f *fakeForge) Create(opts forge.CreateOptions) (*forge.PR, error) {
	if f.failOn != "" && strings.Contains(opts.Dir, f.failOn) {
		return nil, fmt.Errorf("pull request refused")
	}
	body, err := os.ReadFile(opts.BodyFile)
	if err != nil {
		return nil, err
	}
	f.bodies[opts.Dir] = string(body)
	f.created = append(f.created, opts)
	f.next++
	pr := &forge.PR{
		Number: f.next,
		URL:    fmt.Sprintf("https://github.test/pull/%d", f.next),
		State:  "OPEN",
	}
	f.existing[opts.Dir+"\x00"+opts.Head] = pr
	return pr, nil
}

func (f *fakeForge) EditBody(dir, _ string, bodyFile string) error {
	body, err := os.ReadFile(bodyFile)
	if err != nil {
		return err
	}
	f.bodies[dir] = string(body)
	f.edits++
	return nil
}

// commitEverything is an Asker that always commits, with a fixed message.
func commitEverything(message string) Asker {
	return func(Repo, gitx.Status) (CommitDecision, string, error) {
		return CommitChanges, message, nil
	}
}

func remoteBranches(t *testing.T, cfg configLike, repo string) string {
	t.Helper()
	out, err := gitx.Run(cfg.RepoPath(repo), "ls-remote", "--heads", "origin")
	if err != nil {
		t.Fatalf("ls-remote: %v", err)
	}
	return out
}

// configLike is the sliver of config the helpers need.
type configLike interface{ RepoPath(string) string }

func TestPublishPushesAndOpensOnePRPerRepo(t *testing.T) {
	cfg := env(t, "nm", "site")
	task, err := Create(cfg, Options{Name: "auth", Repos: []string{"nm", "site"}})
	if err != nil {
		t.Fatal(err)
	}
	task.Prompt = "Refactor auth across both repos."
	if err := task.Save(); err != nil {
		t.Fatal(err)
	}

	for _, r := range task.Repos {
		writeInto(t, r.Dir, "change.txt", "work\n")
		git(t, r.Dir, "add", "-A")
		git(t, r.Dir, "commit", "-m", "do the "+r.Name+" half")
	}

	gh := newFakeForge()
	results, err := Publish(&task, PublishOptions{GH: gh})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("got %d results, want one per repo", len(results))
	}
	for _, r := range results {
		if r.State != PROpened {
			t.Errorf("%s: state %q (%s), want opened", r.Repo, r.State, r.Reason)
		}
		if r.URL == "" {
			t.Errorf("%s: no pull request URL", r.Repo)
		}
	}

	// The branches actually reached their remotes.
	for _, repo := range []string{"nm", "site"} {
		if heads := remoteBranches(t, cfg, repo); !strings.Contains(heads, task.Repos[0].Branch) {
			t.Errorf("%s: branch %s is not on the remote:\n%s", repo, task.Repos[0].Branch, heads)
		}
	}

	// Each PR went into the branch the worktree was cut from.
	for _, opts := range gh.created {
		if opts.Base != "main" {
			t.Errorf("base = %q, want main", opts.Base)
		}
		if opts.Title != "auth" {
			t.Errorf("title = %q, want the task name", opts.Title)
		}
		if opts.Head != task.Repos[0].Branch {
			t.Errorf("head = %q, want the task branch", opts.Head)
		}
	}

	// The record remembers where the reviews are.
	saved, err := Load(task.Dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range saved.Repos {
		if r.PRURL == "" || r.PRNumber == 0 {
			t.Errorf("%s: the task record did not keep its pull request: %+v", r.Name, r)
		}
	}

	// Two repos means each body gains a link to the other.
	if gh.edits != 2 {
		t.Errorf("cross-linked %d bodies, want 2", gh.edits)
	}
	for dir, body := range gh.bodies {
		if !strings.Contains(body, "Part of task") {
			t.Errorf("%s body has no sibling section:\n%s", dir, body)
		}
		if !strings.Contains(body, "Refactor auth across both repos.") {
			t.Errorf("%s body lost the task prompt:\n%s", dir, body)
		}
		if !strings.Contains(body, "do the ") {
			t.Errorf("%s body lists no commits:\n%s", dir, body)
		}
	}
}

func TestPublishCommitsWhenAsked(t *testing.T) {
	cfg := env(t, "nm")
	task, err := Create(cfg, Options{Name: "wip", Repos: []string{"nm"}})
	if err != nil {
		t.Fatal(err)
	}
	// Uncommitted, including an untracked file.
	writeInto(t, task.Repos[0].Dir, "README.md", "edited\n")
	writeInto(t, task.Repos[0].Dir, "brand-new.txt", "new\n")

	gh := newFakeForge()
	results, err := Publish(&task, PublishOptions{GH: gh, Ask: commitEverything("save the work")})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if results[0].State != PROpened {
		t.Fatalf("state = %q (%s), want opened", results[0].State, results[0].Reason)
	}

	status, err := gitx.GetStatus(task.Repos[0].Dir)
	if err != nil {
		t.Fatal(err)
	}
	if status.Dirty() {
		t.Errorf("work is still uncommitted after publishing: %+v", status)
	}
	body := gh.bodies[task.Repos[0].Dir]
	if !strings.Contains(body, "save the work") {
		t.Errorf("the commit is missing from the body:\n%s", body)
	}
}

func TestPublishRefusesUncommittedWorkWithNoAsker(t *testing.T) {
	cfg := env(t, "nm")
	task, err := Create(cfg, Options{Name: "wip", Repos: []string{"nm"}})
	if err != nil {
		t.Fatal(err)
	}
	writeInto(t, task.Repos[0].Dir, "README.md", "edited\n")

	gh := newFakeForge()
	results, err := Publish(&task, PublishOptions{GH: gh})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if results[0].State != PRFailed {
		t.Errorf("state = %q, want failed when nobody can answer", results[0].State)
	}
	if len(gh.created) != 0 {
		t.Error("a pull request was opened despite uncommitted work")
	}
}

func TestPublishSkipsRepoWithNothingToReview(t *testing.T) {
	cfg := env(t, "nm", "site")
	task, err := Create(cfg, Options{Name: "half", Repos: []string{"nm", "site"}})
	if err != nil {
		t.Fatal(err)
	}
	// Only one repo has work.
	writeInto(t, task.Repos[0].Dir, "change.txt", "work\n")
	git(t, task.Repos[0].Dir, "add", "-A")
	git(t, task.Repos[0].Dir, "commit", "-m", "only here")

	gh := newFakeForge()
	results, err := Publish(&task, PublishOptions{GH: gh})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if results[0].State != PROpened {
		t.Errorf("the repo with commits: %q (%s)", results[0].State, results[0].Reason)
	}
	if results[1].State != PRSkipped || results[1].Reason != "no commits to review" {
		t.Errorf("the untouched repo: %q (%s), want skipped", results[1].State, results[1].Reason)
	}
	if len(gh.created) != 1 {
		t.Errorf("opened %d pull requests, want 1", len(gh.created))
	}
	// A single PR has no siblings to link to.
	if gh.edits != 0 {
		t.Errorf("cross-linked %d bodies for a lone pull request", gh.edits)
	}
}

func TestPublishReusesAnExistingPullRequest(t *testing.T) {
	cfg := env(t, "nm")
	task, err := Create(cfg, Options{Name: "again", Repos: []string{"nm"}})
	if err != nil {
		t.Fatal(err)
	}
	writeInto(t, task.Repos[0].Dir, "change.txt", "work\n")
	git(t, task.Repos[0].Dir, "add", "-A")
	git(t, task.Repos[0].Dir, "commit", "-m", "work")

	gh := newFakeForge()
	if _, err := Publish(&task, PublishOptions{GH: gh}); err != nil {
		t.Fatal(err)
	}
	firstCount := len(gh.created)

	// Running it again pushes but must not open a second pull request.
	writeInto(t, task.Repos[0].Dir, "more.txt", "more\n")
	git(t, task.Repos[0].Dir, "add", "-A")
	git(t, task.Repos[0].Dir, "commit", "-m", "more work")

	results, err := Publish(&task, PublishOptions{GH: gh})
	if err != nil {
		t.Fatal(err)
	}
	if results[0].State != PRExisted {
		t.Errorf("state = %q, want existed on the second run", results[0].State)
	}
	if len(gh.created) != firstCount {
		t.Errorf("a second pull request was opened (%d total)", len(gh.created))
	}
}

func TestPublishStopsWhenAuthFails(t *testing.T) {
	cfg := env(t, "nm")
	task, err := Create(cfg, Options{Name: "noauth", Repos: []string{"nm"}})
	if err != nil {
		t.Fatal(err)
	}
	gh := newFakeForge()
	gh.authErr = forge.ErrNotAuthenticated

	if _, err := Publish(&task, PublishOptions{GH: gh}); err == nil {
		t.Fatal("Publish ran without a login")
	}
	if heads := remoteBranches(t, cfg, "nm"); strings.Contains(heads, task.Repos[0].Branch) {
		t.Error("the branch was pushed even though the login check failed")
	}
}

func TestPublishReportsAFailedRepo(t *testing.T) {
	cfg := env(t, "nm", "site")
	task, err := Create(cfg, Options{Name: "partial", Repos: []string{"nm", "site"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range task.Repos {
		writeInto(t, r.Dir, "change.txt", "work\n")
		git(t, r.Dir, "add", "-A")
		git(t, r.Dir, "commit", "-m", "work")
	}

	gh := newFakeForge()
	gh.failOn = "site-"

	results, err := Publish(&task, PublishOptions{GH: gh})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if results[0].State != PROpened {
		t.Errorf("the healthy repo: %q", results[0].State)
	}
	if results[1].State != PRFailed {
		t.Errorf("the failing repo: %q, want failed", results[1].State)
	}
	if !results[0].OK() || results[1].OK() {
		t.Error("OK() does not distinguish the two outcomes")
	}
	// What did succeed is still recorded, so a re-run picks up from there.
	saved, err := Load(task.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Repos[0].PRURL == "" {
		t.Error("the successful pull request was not recorded")
	}
}

func TestPRBody(t *testing.T) {
	task := Task{Name: "auth", Hash: "9c31a0", Dir: "/tasks/auth-9c31a0", Prompt: "Refactor auth."}
	repo := Repo{Name: "nm"}
	siblings := []PRResult{
		{Repo: "nm", URL: "https://github.test/pull/1"},
		{Repo: "site", URL: "https://github.test/pull/2"},
	}

	body := PRBody(task, repo, []string{"first commit", "second commit"}, siblings)

	for _, want := range []string{
		"Refactor auth.",
		"## Commits",
		"- first commit",
		"- second commit",
		"Part of task `auth-9c31a0`",
		"site: https://github.test/pull/2",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body is missing %q:\n%s", want, body)
		}
	}
	// A repo does not link to itself.
	if strings.Contains(body, "nm: https://github.test/pull/1") {
		t.Errorf("body links to its own pull request:\n%s", body)
	}

	// A task with no prompt and no siblings still produces something sane.
	bare := PRBody(Task{Name: "x", Hash: "abc", Dir: "/tasks/x-abc"}, repo, []string{"only commit"}, nil)
	if strings.Contains(bare, "Part of task") {
		t.Errorf("a lone pull request got a siblings section:\n%s", bare)
	}
	if !strings.Contains(bare, "- only commit") {
		t.Errorf("commits are missing:\n%s", bare)
	}
}

func writeInto(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
