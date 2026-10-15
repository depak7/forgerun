// Package publish pushes a task's changes to a branch and opens or updates
// its pull request. It runs on the forgerun host with the host's git; the
// token never enters a VM.
package publish

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/depak7/forgerun/internal/forge"
	"github.com/depak7/forgerun/internal/task"
)

// Publisher pushes a branch with a task's changes and, when asked, opens or
// updates a pull request.
type Publisher struct {
	Tokens forge.TokenSource
	GitHub *forge.GitHub
	// RemoteURL is where to fetch and push; nil = https://host/owner/name.git.
	// Tests point it at a local bare repository.
	RemoteURL func(forge.Repo) string
	// TaskURL links a pull request back to its task; nil = no link.
	TaskURL func(taskID string) string
}

var _ task.Publisher = (*Publisher)(nil)

const (
	authorName  = "forgerun"
	authorEmail = "forgerun@users.noreply.github.com"
	maxTitle    = 72 // characters, as git and GitHub display them comfortably
)

// maxSlug bounds the words of a branch name taken from the prompt.
const maxSlug = 40

// Branch is the branch a thread's changes are pushed to: a few words of the
// prompt and the end of the task ID, e.g. forgerun/add-a-hello-world-9fe9cc.
// The ID's random tail keeps two tasks with the same prompt apart, so a push
// never lands on someone else's branch (pushes are never forced).
func Branch(taskID, prompt string) string {
	slug := slugify(prompt)
	suffix := taskID[max(0, len(taskID)-6):]
	if slug == "" {
		return "forgerun/" + suffix
	}
	return "forgerun/" + slug + "-" + suffix
}

// slugify keeps the first words of s as lower-case ASCII letters and
// digits joined by "-", at most maxSlug bytes, cut at a word boundary.
func slugify(s string) string {
	var words []string
	n := 0
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	}) {
		if n+len(w) > maxSlug {
			if n == 0 { // one very long word: cut it
				words, n = append(words, w[:maxSlug]), maxSlug
			}
			break
		}
		words = append(words, w)
		n += len(w) + 1
	}
	return strings.Join(words, "-")
}

// Publish commits the task's changes on top of the commit the task started
// from and pushes them (never forced). A fresh task gets a new branch; a
// follow-up adds a commit to its thread's branch. Then, if the thread has a
// pull request, its description is refreshed; if not and one is wanted, it
// is opened into the repo's default branch.
func (p *Publisher) Publish(ctx context.Context, req task.PublishRequest) (task.Published, error) {
	repo, err := forge.ParseRepo(req.Repo)
	if err != nil {
		return task.Published{}, err
	}
	tok, err := p.Tokens.Token(ctx, repo)
	if err != nil {
		return task.Published{}, fmt.Errorf("getting a token for %s: %w", repo, err)
	}

	dir, err := os.MkdirTemp("", "forgerun-publish-")
	if err != nil {
		return task.Published{}, err
	}
	defer os.RemoveAll(dir)
	g := &git{ctx: ctx, dir: dir, env: authEnv(repo, tok), secret: tok.Reveal()}
	// The patch goes inside .git, so it's never part of the commit.
	patch := filepath.Join(dir, ".git", "forgerun.patch")

	followUp := req.Branch != ""
	branch := cmp.Or(req.Branch, Branch(req.TaskID, req.Prompt))
	remote := "https://" + repo.Host + "/" + repo.Owner + "/" + repo.Name + ".git"
	if p.RemoteURL != nil {
		remote = p.RemoteURL(repo)
	}
	// Only the commit the task started from is needed: for a fresh task,
	// even if the default branch has moved on since; for a follow-up, the
	// branch's commit when the task began.
	if _, err := g.run("init", "-q"); err != nil {
		return task.Published{}, err
	}
	if err := os.WriteFile(patch, []byte(req.Patch), 0o600); err != nil {
		return task.Published{}, err
	}
	steps := [][]string{
		{"remote", "add", "origin", remote},
		{"fetch", "-q", "--depth=1", "origin", req.BaseCommit},
		{"checkout", "-q", "-b", branch, "FETCH_HEAD"},
		{"apply", "--index", patch},
		{"commit", "-q", "-m", commitMessage(req)},
	}
	for _, args := range steps {
		if _, err := g.run(args...); err != nil {
			return task.Published{}, err
		}
	}
	files, err := g.run("diff", "--name-status", "-M", "HEAD~1", "HEAD")
	if err != nil {
		return task.Published{}, err
	}
	// No --force: if the branch has commits we don't have (someone pushed
	// while the task ran), git refuses rather than overwrite them.
	if _, err := g.run("push", "-q", "origin", "HEAD:refs/heads/"+branch); err != nil {
		if followUp && strings.Contains(err.Error(), "rejected") {
			return task.Published{}, fmt.Errorf("branch %s changed while the task ran (did someone push to it?); nothing was overwritten: %w", branch, err)
		}
		return task.Published{}, err
	}
	pub := task.Published{Branch: branch}

	body := p.body(req, files)
	var pr forge.PullRequest
	switch {
	case req.PRNumber > 0:
		pr, err = p.GitHub.UpdatePullRequestBody(ctx, tok, repo, req.PRNumber, body)
		if err != nil {
			return pub, fmt.Errorf("the commit was pushed to %s, but updating pull request #%d failed: %w", branch, req.PRNumber, err)
		}
	case req.OpenPR:
		base, err := p.GitHub.DefaultBranch(ctx, tok, repo)
		if err != nil {
			return pub, fmt.Errorf("branch %s was pushed, but opening the pull request failed: %w", branch, err)
		}
		// Named after the thread's first request: a pull request opened by
		// a follow-up still covers the whole thread.
		first := req.Prompt
		if len(req.Requests) > 0 {
			first = req.Requests[0]
		}
		pr, err = p.GitHub.OpenPullRequest(ctx, tok, repo, branch, base, title(first), body)
		if err != nil {
			return pub, fmt.Errorf("branch %s was pushed, but opening the pull request failed: %w", branch, err)
		}
	default:
		return pub, nil // a branch only
	}
	pub.PRURL, pub.PRNumber = pr.URL, pr.Number
	return pub, nil
}

// IsOpen reports whether a repo's pull request is still open, so a
// follow-up can be refused before any work is done.
func (p *Publisher) IsOpen(ctx context.Context, repoURL string, number int) (bool, error) {
	repo, err := forge.ParseRepo(repoURL)
	if err != nil {
		return false, err
	}
	tok, err := p.Tokens.Token(ctx, repo)
	if err != nil {
		return false, err
	}
	pr, err := p.GitHub.GetPullRequest(ctx, tok, repo, number)
	if err != nil {
		return false, err
	}
	return pr.State == "open", nil
}

// authEnv gives git the token through its environment only: never in the
// remote URL or on the command line, where other processes could see it.
func authEnv(repo forge.Repo, tok forge.Token) []string {
	basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + tok.Reveal()))
	return []string{
		"GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=http.https://" + repo.Host + "/.extraheader",
		"GIT_CONFIG_VALUE_0=Authorization: Basic " + basic,
		"GIT_CONFIG_KEY_1=credential.helper", // don't consult the OS keychain
		"GIT_CONFIG_VALUE_1=",
		"GIT_TERMINAL_PROMPT=0", // fail instead of asking for a password
		"GIT_AUTHOR_NAME=" + authorName, "GIT_AUTHOR_EMAIL=" + authorEmail,
		"GIT_COMMITTER_NAME=" + authorName, "GIT_COMMITTER_EMAIL=" + authorEmail,
	}
}

// git runs git commands in one directory with one environment.
type git struct {
	ctx    context.Context
	dir    string
	env    []string
	secret string // scrubbed from any output that ends up in an error
}

func (g *git) run(args ...string) (string, error) {
	cmd := exec.CommandContext(g.ctx, "git", args...)
	cmd.Dir = g.dir
	cmd.Env = append(os.Environ(), g.env...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %s", args[0], g.scrub(strings.TrimSpace(out.String())))
	}
	return out.String(), nil
}

// scrub removes the token, in plain and in base64 form, from git output.
func (g *git) scrub(s string) string {
	if g.secret == "" {
		return s
	}
	s = strings.ReplaceAll(s, g.secret, "[redacted]")
	return strings.ReplaceAll(s, base64.StdEncoding.EncodeToString([]byte("x-access-token:"+g.secret)), "[redacted]")
}

// title is the prompt's first line, shortened to maxTitle characters.
func title(prompt string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(prompt), "\n")
	line = strings.TrimSpace(line)
	if utf8.RuneCountInString(line) <= maxTitle {
		return line
	}
	r := []rune(line) // cut by characters, not bytes, so non-ASCII text stays valid
	return strings.TrimSpace(string(r[:maxTitle-1])) + "…"
}

func commitMessage(req task.PublishRequest) string {
	return title(req.Prompt) + "\n\nMade by forgerun task " + req.TaskID + "."
}

// body is the pull request description: the agent's summary when there is
// one, otherwise the requests and the files the latest commit changed.
// Every request in the thread is listed, oldest first.
func (p *Publisher) body(req task.PublishRequest, files string) string {
	requests := req.Requests
	if len(requests) == 0 {
		requests = []string{req.Prompt}
	}
	var b strings.Builder
	if req.Summary != "" {
		b.WriteString(req.Summary + "\n\n")
	} else {
		b.WriteString("## Requested\n\n" + list(requests) + "\n\n## Changed in the latest commit\n\n```\n" + strings.TrimSpace(files) + "\n```\n\n")
	}
	b.WriteString("---\n")
	b.WriteString(cmp.Or(p.taskLink(req.TaskID), "forgerun task `"+req.TaskID+"`"))
	if req.Summary != "" {
		label := "Original request"
		if len(requests) > 1 {
			label = fmt.Sprintf("Requests (%d)", len(requests))
		}
		b.WriteString("\n\n<details><summary>" + label + "</summary>\n\n" + list(requests) + "\n\n</details>")
	}
	return b.String()
}

// list formats requests as block quotes, numbered when there's more than one.
func list(requests []string) string {
	if len(requests) == 1 {
		return quote(requests[0])
	}
	parts := make([]string, len(requests))
	for i, r := range requests {
		parts[i] = fmt.Sprintf("%d.\n%s", i+1, quote(r))
	}
	return strings.Join(parts, "\n\n")
}

func (p *Publisher) taskLink(id string) string {
	if p.TaskURL == nil {
		return ""
	}
	return "[forgerun task `" + id + "`](" + p.TaskURL(id) + ")"
}

// quote formats text as a Markdown block quote.
func quote(s string) string {
	return "> " + strings.ReplaceAll(strings.TrimSpace(s), "\n", "\n> ")
}
