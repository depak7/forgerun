// Package publish turns a task's changes into a pull request. It runs on
// the forgerun host with the host's git; the token never enters a VM.
package publish

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"unicode/utf8"

	"github.com/depak7/forgerun/internal/forge"
	"github.com/depak7/forgerun/internal/task"
)

// Publisher pushes a branch with a task's changes and opens a pull request.
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

// Branch is the branch a task's changes are pushed to.
func Branch(taskID string) string { return "forgerun/" + taskID }

// Publish commits the task's patch on top of the commit the task started
// from, pushes it as a new branch (never forced) and opens a pull request
// into the repo's default branch. It returns the pull request's URL.
func (p *Publisher) Publish(ctx context.Context, req task.PublishRequest) (string, error) {
	repo, err := forge.ParseRepo(req.Repo)
	if err != nil {
		return "", err
	}
	tok, err := p.Tokens.Token(ctx, repo)
	if err != nil {
		return "", fmt.Errorf("getting a token for %s: %w", repo, err)
	}

	dir, err := os.MkdirTemp("", "forgerun-publish-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	g := &git{ctx: ctx, dir: dir, env: authEnv(repo, tok), secret: tok.Reveal()}

	branch := Branch(req.TaskID)
	remote := "https://" + repo.Host + "/" + repo.Owner + "/" + repo.Name + ".git"
	if p.RemoteURL != nil {
		remote = p.RemoteURL(repo)
	}
	// Only the commit the task started from is needed, even if the default
	// branch has moved on since.
	steps := [][]string{
		{"init", "-q"},
		{"remote", "add", "origin", remote},
		{"fetch", "-q", "--depth=1", "origin", req.BaseCommit},
		{"checkout", "-q", "-b", branch, "FETCH_HEAD"},
		{"apply", "--index", req.PatchPath},
		{"commit", "-q", "-m", commitMessage(req)},
	}
	for _, args := range steps {
		if _, err := g.run(args...); err != nil {
			return "", err
		}
	}
	files, err := g.run("diff", "--name-status", "-M", "HEAD~1", "HEAD")
	if err != nil {
		return "", err
	}
	// No --force: if the branch exists with other commits, git refuses
	// rather than overwrite them.
	if _, err := g.run("push", "-q", "origin", "HEAD:refs/heads/"+branch); err != nil {
		return "", err
	}

	base, err := p.GitHub.DefaultBranch(ctx, tok, repo)
	if err != nil {
		return "", err
	}
	pr, err := p.GitHub.OpenPullRequest(ctx, tok, repo, branch, base, title(req.Prompt), p.body(req, files))
	if err != nil {
		return "", fmt.Errorf("branch %s was pushed, but opening the pull request failed: %w", branch, err)
	}
	return pr.URL, nil
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
// one, otherwise the request and the changed files.
func (p *Publisher) body(req task.PublishRequest, files string) string {
	var b strings.Builder
	if req.Summary != "" {
		b.WriteString(req.Summary + "\n\n")
	} else {
		b.WriteString("## Requested\n\n" + quote(req.Prompt) + "\n\n## Changed files\n\n```\n" + strings.TrimSpace(files) + "\n```\n\n")
	}
	b.WriteString("---\n")
	b.WriteString(cmp.Or(p.taskLink(req.TaskID), "forgerun task `"+req.TaskID+"`"))
	if req.Summary != "" {
		b.WriteString("\n\n<details><summary>Original request</summary>\n\n" + quote(req.Prompt) + "\n\n</details>")
	}
	return b.String()
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
