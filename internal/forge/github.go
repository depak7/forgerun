package forge

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ErrBadToken means the forge rejected the token: wrong, revoked or expired.
var ErrBadToken = errors.New("token rejected")

// GitHub is a small client for GitHub's REST API.
type GitHub struct {
	BaseURL string       // "" = https://api.github.com; tests point it at a fake
	HTTP    *http.Client // nil = a client with a 30s timeout
}

// Identity is who a token acts as.
type Identity struct {
	Login   string    // e.g. "depak7"
	Expires time.Time // zero if the token never expires
}

// WhoAmI checks a token and returns who it belongs to and when it expires.
func (g *GitHub) WhoAmI(ctx context.Context, tok Token) (Identity, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.url("/user"), nil)
	if err != nil {
		return Identity{}, err
	}
	req.Header.Set("Authorization", "Bearer "+tok.Reveal())
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := g.client().Do(req)
	if err != nil {
		return Identity{}, fmt.Errorf("asking GitHub who the token belongs to: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode == http.StatusUnauthorized {
		return Identity{}, fmt.Errorf("%w by GitHub: %s", ErrBadToken, message(body))
	}
	if resp.StatusCode != http.StatusOK {
		return Identity{}, fmt.Errorf("GitHub /user: HTTP %d: %s", resp.StatusCode, message(body))
	}
	var user struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(body, &user); err != nil || user.Login == "" {
		return Identity{}, fmt.Errorf("GitHub /user: unexpected response")
	}
	id := Identity{Login: user.Login}
	// Fine-grained and classic tokens with an expiry report it in a header,
	// e.g. "2026-10-21 07:52:48 UTC".
	if exp := resp.Header.Get("GitHub-Authentication-Token-Expiration"); exp != "" {
		if id.Expires, err = parseExpiry(exp); err != nil {
			return Identity{}, fmt.Errorf("GitHub token expiry %q: %w", exp, err)
		}
	}
	return id, nil
}

func parseExpiry(s string) (time.Time, error) {
	for _, layout := range []string{"2006-01-02 15:04:05 MST", "2006-01-02 15:04:05 -0700"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, errors.New("unknown time format")
}

// message pulls GitHub's error message out of a JSON error body.
func message(body []byte) string {
	var e struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &e) == nil && e.Message != "" {
		return e.Message
	}
	return strings.TrimSpace(string(body))
}

func (g *GitHub) url(path string) string {
	return strings.TrimRight(cmp.Or(g.BaseURL, "https://api.github.com"), "/") + path
}

func (g *GitHub) client() *http.Client {
	if g.HTTP != nil {
		return g.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// DefaultBranch returns the repo's default branch, e.g. "main".
func (g *GitHub) DefaultBranch(ctx context.Context, tok Token, repo Repo) (string, error) {
	var r struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := g.call(ctx, tok, http.MethodGet, "/repos/"+repo.Owner+"/"+repo.Name, nil, &r); err != nil {
		return "", err
	}
	return r.DefaultBranch, nil
}

// PullRequest is the part of a new pull request forgerun keeps.
type PullRequest struct {
	Number int    `json:"number"`
	URL    string `json:"html_url"`
}

// OpenPullRequest opens a pull request from branch head into base.
func (g *GitHub) OpenPullRequest(ctx context.Context, tok Token, repo Repo, head, base, title, body string) (PullRequest, error) {
	in := map[string]string{"head": head, "base": base, "title": title, "body": body}
	var pr PullRequest
	err := g.call(ctx, tok, http.MethodPost, "/repos/"+repo.Owner+"/"+repo.Name+"/pulls", in, &pr)
	return pr, err
}

// call sends one API request with an optional JSON body and decodes the
// JSON reply into out. Errors carry GitHub's own explanation.
func (g *GitHub) call(ctx context.Context, tok Token, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.url(path), body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok.Reveal())
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := g.client().Do(req)
	if err != nil {
		return fmt.Errorf("GitHub %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode >= 300 {
		msg := detail(data)
		if resp.StatusCode == http.StatusUnauthorized {
			return fmt.Errorf("%w by GitHub: %s", ErrBadToken, msg)
		}
		// For a missing permission, GitHub names the one it wanted.
		if need := resp.Header.Get("X-Accepted-GitHub-Permissions"); resp.StatusCode == http.StatusForbidden && need != "" {
			msg += " (the token needs: " + need + ")"
		}
		return fmt.Errorf("GitHub %s %s: HTTP %d: %s", method, path, resp.StatusCode, msg)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("GitHub %s %s: unexpected response: %w", method, path, err)
		}
	}
	return nil
}

// detail is GitHub's error message plus any per-field reasons, e.g.
// "Validation Failed: A pull request already exists for depak7:branch."
func detail(body []byte) string {
	var e struct {
		Message string `json:"message"`
		Errors  []struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"errors"`
	}
	if json.Unmarshal(body, &e) != nil || e.Message == "" {
		return strings.TrimSpace(string(body))
	}
	var reasons []string
	for _, fe := range e.Errors {
		reasons = append(reasons, cmp.Or(fe.Message, fe.Code))
	}
	if len(reasons) == 0 {
		return e.Message
	}
	return e.Message + ": " + strings.Join(reasons, "; ")
}
