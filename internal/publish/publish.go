// Package publish pushes a task's changes to a branch and opens or updates
// its pull request. It runs on the forgerun host with the host's git; the
// token never enters a VM.
package publish

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"errors"
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
	// ThreadURL links a pull request back to its thread; nil = no link.
	ThreadURL func(threadID string) string
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

// Publish applies the thread's unpushed changes on top of its branch (or,
// for a new branch, the commit the thread started from) and pushes them as
// one commit, never forced. Then, if the thread has a pull request, its
// description is refreshed; if not and one is wanted, it is opened into
// the repo's default branch.
func (p *Publisher) Publish(ctx context.Context, req task.PublishRequest) (task.Published, error) {
	repo, err := forge.ParseRepo(req.Repo)
	if err != nil {
		return task.Published{}, err
	}
	if len(req.Requests) == 0 {
		return task.Published{}, errors.New("publishing needs the thread's requests")
	}
	tok, err := p.token(ctx, repo, req.Token)
	if err != nil {
		return task.Published{}, fmt.Errorf("getting a token for %s: %w", repo, err)
	}

	dir, err := os.MkdirTemp("", "forgerun-publish-")
	if err != nil {
		return task.Published{}, err
	}
	defer os.RemoveAll(dir)
	g := &git{ctx: ctx, dir: dir, env: authEnv(repo, tok), secret: tok.Reveal()}

	existing := req.Branch != ""
	branch := cmp.Or(req.Branch, Branch(req.ThreadID, req.Requests[0]))
	pub := task.Published{Branch: branch}
	remote := "https://" + repo.Host + "/" + repo.Owner + "/" + repo.Name + ".git"
	if p.RemoteURL != nil {
		remote = p.RemoteURL(repo)
	}
	var files string
	if len(req.Patches) > 0 {
		// Only one commit is needed: the branch's head, or the commit the
		// thread started from (even if the default branch has moved on).
		from := req.BaseCommit
		if existing {
			from = branch
		}
		steps := [][]string{
			{"init", "-q"},
			{"remote", "add", "origin", remote},
			{"fetch", "-q", "--depth=1", "origin", from},
			{"checkout", "-q", "-b", branch, "FETCH_HEAD"},
		}
		for _, args := range steps {
			if _, err := g.run(args...); err != nil {
				return task.Published{}, err
			}
		}
		// Each turn's changes in order; the patches go inside .git, so
		// they're never part of the commit.
		patch := filepath.Join(dir, ".git", "forgerun.patch")
		for i, diff := range req.Patches {
			if err := os.WriteFile(patch, []byte(diff), 0o600); err != nil {
				return task.Published{}, err
			}
			if _, err := g.run("apply", "--index", patch); err != nil {
				return task.Published{}, fmt.Errorf("applying change %d of %d: %w", i+1, len(req.Patches), err)
			}
		}
		if _, err := g.run("commit", "-q", "-m", commitMessage(req)); err != nil {
			return task.Published{}, err
		}
		if files, err = g.run("diff", "--name-status", "-M", "HEAD~1", "HEAD"); err != nil {
			return task.Published{}, err
		}
		// No --force: if the branch has commits we don't have (someone
		// pushed since we fetched it), git refuses rather than overwrite them.
		if _, err := g.run("push", "-q", "origin", "HEAD:refs/heads/"+branch); err != nil {
			if existing && strings.Contains(err.Error(), "rejected") {
				return task.Published{}, fmt.Errorf("branch %s changed while pushing (did someone push to it?); nothing was overwritten: %w", branch, err)
			}
			return task.Published{}, err
		}
	} else if !existing {
		return task.Published{}, errors.New("nothing to push, and the thread has no branch yet")
	}

	body := p.body(req, files)
	var pr forge.PullRequest
	switch {
	case req.PRNumber > 0:
		pr, err = p.GitHub.UpdatePullRequestBody(ctx, tok, repo, req.PRNumber, body)
		if err != nil {
			return pub, fmt.Errorf("branch %s is pushed, but updating pull request #%d failed: %w", branch, req.PRNumber, err)
		}
	case req.OpenPR:
		base, err := p.GitHub.DefaultBranch(ctx, tok, repo)
		if err != nil {
			return pub, fmt.Errorf("branch %s is pushed, but opening the pull request failed: %w", branch, err)
		}
		// Named after the thread's first request: it covers the whole thread.
		pr, err = p.GitHub.OpenPullRequest(ctx, tok, repo, branch, base, title(req.Requests[0]), body)
		if err != nil {
			return pub, fmt.Errorf("branch %s is pushed, but opening the pull request failed: %w", branch, err)
		}
	default:
		return pub, nil // a branch only
	}
	pub.PRURL, pub.PRNumber = pr.URL, pr.Number
	return pub, nil
}

// IsOpen reports whether a repo's pull request is still open, so a
// follow-up can be refused before any work is done.
func (p *Publisher) IsOpen(ctx context.Context, repoURL string, number int, user forge.Token) (bool, error) {
	repo, err := forge.ParseRepo(repoURL)
	if err != nil {
		return false, err
	}
	tok, err := p.token(ctx, repo, user)
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

// commitMessage titles a push after the thread's first request and lists
// the requests it covers.
func commitMessage(req task.PublishRequest) string {
	msg := title(req.Requests[0])
	if len(req.Requests) > 1 {
		msg += "\n\nRequests:\n"
		for _, r := range req.Requests {
			msg += "- " + title(r) + "\n"
		}
	}
	if len(req.Requests) == 1 {
		msg += "\n"
	}
	return msg + "\nMade by forgerun thread " + req.ThreadID + "."
}

// body is the pull request description: the agent's summary when there is
// one, otherwise the requests and the files the latest commit changed.
// Every request in the thread is listed, oldest first.
func (p *Publisher) body(req task.PublishRequest, files string) string {
	requests := req.Requests
	var b strings.Builder
	if req.Summary != "" {
		b.WriteString(req.Summary + "\n\n")
	} else {
		b.WriteString("## Requested\n\n" + list(requests) + "\n\n")
		if files = strings.TrimSpace(files); files != "" {
			b.WriteString("## Changed in the latest commit\n\n```\n" + files + "\n```\n\n")
		}
	}
	b.WriteString("---\n")
	b.WriteString(cmp.Or(p.threadLink(req.ThreadID), "forgerun thread `"+req.ThreadID+"`"))
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

func (p *Publisher) threadLink(id string) string {
	if p.ThreadURL == nil {
		return ""
	}
	return "[forgerun thread `" + id + "`](" + p.ThreadURL(id) + ")"
}

// quote formats text as a Markdown block quote.
func quote(s string) string {
	return "> " + strings.ReplaceAll(strings.TrimSpace(s), "\n", "\n> ")
}

// token is the user's token if there is one, otherwise the server's.
func (p *Publisher) token(ctx context.Context, repo forge.Repo, user forge.Token) (forge.Token, error) {
	if !user.IsZero() {
		return user, nil
	}
	if p.Tokens == nil {
		return forge.Token{}, errors.New("no GitHub token for this push: sign in, or set GITHUB_TOKEN on the server")
	}
	return p.Tokens.Token(ctx, repo)
}

// Access reports what a token (the user's, or the server's) may do with a repo.
func (p *Publisher) Access(ctx context.Context, repoURL string, user forge.Token) (forge.Access, error) {
	repo, err := forge.ParseRepo(repoURL)
	if err != nil {
		return forge.Access{}, err
	}
	tok, err := p.token(ctx, repo, user)
	if err != nil {
		return forge.Access{}, err
	}
	return p.GitHub.RepoAccess(ctx, tok, repo)
}

// Repos lists the repos a token (the user's, or the server's) can push to.
func (p *Publisher) Repos(ctx context.Context, user forge.Token) ([]forge.RepoInfo, error) {
	tok := user
	if tok.IsZero() {
		if p.Tokens == nil {
			return nil, nil
		}
		var err error
		if tok, err = p.Tokens.Token(ctx, forge.Repo{}); err != nil {
			return nil, err
		}
	}
	return p.GitHub.PushableRepos(ctx, tok)
}
