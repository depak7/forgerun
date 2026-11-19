package handoff

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client talks to a forgerun server's API, as the user whose personal
// token it has ("" for a server in local mode, which has no sign-in).
type Client struct {
	URL   string // e.g. https://forgerun.example.com
	Token string
	HTTP  *http.Client // nil = a client with a generous timeout, for uploads
	// Poll is how often forgerun_status checks a running turn while it
	// waits; 0 = every 3 seconds.
	Poll time.Duration
}

// Task is a turn, as the API reports it (the fields the tools need).
type Task struct {
	ID         string `json:"id"`
	ThreadID   string `json:"thread_id"`
	Repo       string `json:"repo"`
	Folder     string `json:"folder"`
	BaseBranch string `json:"base_branch"`
	Agent      string `json:"agent"`
	ModelID    string `json:"model_id"`
	Prompt     string `json:"prompt"`
	State      string `json:"state"`
	Error      string `json:"error"`
	HasDiff    bool   `json:"has_diff"`
	Reply      string `json:"reply"`
	Plan       bool   `json:"plan"`
	Asked      bool   `json:"asked"`
	PR         struct {
		URL    string `json:"url"`
		Branch string `json:"branch"`
		Error  string `json:"error"`
	} `json:"pr"`
}

// Start is a thread's first message (on a repo, or an uploaded folder), or
// a follow-up's.
type Start struct {
	Repo   string `json:"repo,omitempty"`
	Branch string `json:"branch,omitempty"` // the repo's branch; "" = its default
	Folder string `json:"folder,omitempty"` // an uploaded folder's ID, in place of Repo
	Prompt string `json:"prompt"`
	Agent  string `json:"agent,omitempty"` // "" = the server's default (a follow-up: the thread's)
	Model  string `json:"model,omitempty"` // "" = the agent's default
	Effort string `json:"effort,omitempty"`
	Plan   bool   `json:"plan,omitempty"` // plan first: the agent proposes and waits
}

// Agent is an agent the server can run, and whether its key is there.
type Agent struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	DefaultModel string `json:"default_model"`
	Runnable     bool   `json:"runnable"`
	Credential   *struct {
		Name     string `json:"name"`
		Set      bool   `json:"set"`
		Optional bool   `json:"optional"` // some models run without it
	} `json:"credential"`
}

// Model is one of an agent's models (a family, with its efforts).
type Model struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Efforts []string `json:"efforts"`
	Keyless bool     `json:"keyless"`
}

// Thread is one row of the user's threads.
type Thread struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	Repo           string `json:"repo"`
	Steps          int    `json:"steps"`
	Latest         Task   `json:"latest"`
	NeedsAttention bool   `json:"needs_attention"`
	UpdatedAt      string `json:"updated_at"`
}

// Upload sends a packed folder and returns its ID.
func (c *Client) Upload(ctx context.Context, pk Packed) (string, error) {
	q := url.Values{"name": {pk.Name}}
	if pk.Repo != "" {
		q.Set("repo", pk.Repo)
		q.Set("base", pk.Base)
		q.Set("branch", pk.Branch)
	}
	var f struct{ ID string }
	err := c.call(ctx, http.MethodPost, "/api/folders?"+q.Encode(), "application/gzip", bytes.NewReader(pk.Data), &f)
	return f.ID, err
}

// StartThread starts a thread on s.Repo or s.Folder.
func (c *Client) StartThread(ctx context.Context, s Start) (Task, error) {
	return c.postTask(ctx, "/api/tasks", s)
}

// FollowUp sends the next message of the thread whose latest turn is id.
func (c *Client) FollowUp(ctx context.Context, id string, s Start) (Task, error) {
	return c.postTask(ctx, "/api/tasks/"+url.PathEscape(id)+"/followups", s)
}

// Cancel stops a queued or running turn.
func (c *Client) Cancel(ctx context.Context, id string) (Task, error) {
	var t Task
	err := c.call(ctx, http.MethodPost, "/api/tasks/"+url.PathEscape(id)+"/cancel", "", nil, &t)
	return t, err
}

// Push pushes a thread's unpushed changes to its branch (threadID is its
// first turn's), opening a pull request if pr.
func (c *Client) Push(ctx context.Context, threadID string, pr bool) ([]Task, error) {
	body, _ := json.Marshal(map[string]bool{"pr": pr})
	var ts []Task
	err := c.call(ctx, http.MethodPost, "/api/threads/"+url.PathEscape(threadID)+"/push", "application/json", bytes.NewReader(body), &ts)
	return ts, err
}

// Agents lists the agents the server can run.
func (c *Client) Agents(ctx context.Context) ([]Agent, error) {
	var as []Agent
	err := c.call(ctx, http.MethodGet, "/api/agents", "", nil, &as)
	return as, err
}

// Models lists an agent's models.
func (c *Client) Models(ctx context.Context, agent string) ([]Model, error) {
	var l struct{ Models []Model }
	err := c.call(ctx, http.MethodGet, "/api/models?agent="+url.QueryEscape(agent), "", nil, &l)
	return l.Models, err
}

// Threads lists the user's threads, most recent activity first.
func (c *Client) Threads(ctx context.Context, limit int) ([]Thread, error) {
	var ts []Thread
	err := c.call(ctx, http.MethodGet, fmt.Sprintf("/api/threads?limit=%d", limit), "", nil, &ts)
	return ts, err
}

// Branches returns a repo's default branch and up to 100 of its branches.
func (c *Client) Branches(ctx context.Context, repo string) (string, []string, error) {
	var b struct {
		Default  string
		Branches []string
	}
	err := c.call(ctx, http.MethodGet, "/api/repos/branches?repo="+url.QueryEscape(repo), "", nil, &b)
	return b.Default, b.Branches, err
}

// Log returns the end of a turn's log: its last n bytes.
func (c *Client) Log(ctx context.Context, id string, n int) (string, error) {
	return c.text(ctx, "/api/tasks/"+url.PathEscape(id)+"/log", http.Header{"Range": {fmt.Sprintf("bytes=-%d", n)}})
}

func (c *Client) postTask(ctx context.Context, path string, s Start) (Task, error) {
	body, _ := json.Marshal(s)
	var t Task
	err := c.call(ctx, http.MethodPost, path, "application/json", bytes.NewReader(body), &t)
	return t, err
}

// Thread returns a thread's turns, oldest first; id is any turn's.
func (c *Client) Thread(ctx context.Context, id string) ([]Task, error) {
	var ts []Task
	err := c.call(ctx, http.MethodGet, "/api/tasks/"+url.PathEscape(id)+"/thread", "", nil, &ts)
	return ts, err
}

// Diff returns one turn's changes, as git diff output.
func (c *Client) Diff(ctx context.Context, id string) (string, error) {
	return c.text(ctx, "/api/tasks/"+url.PathEscape(id)+"/diff", nil)
}

// text GETs a plain-text answer.
func (c *Client) text(ctx context.Context, path string, h http.Header) (string, error) {
	resp, err := c.do(ctx, http.MethodGet, path, "", nil, h)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

// ThreadURL is a thread's page in forgerun's web UI.
func (c *Client) ThreadURL(threadID string) string {
	return strings.TrimRight(c.URL, "/") + "/threads/" + url.PathEscape(threadID)
}

func (c *Client) do(ctx context.Context, method, path, contentType string, body io.Reader, h http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.URL, "/")+path, body)
	if err != nil {
		return nil, err
	}
	for k, v := range h {
		req.Header[k] = v
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 5 * time.Minute}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reaching forgerun at %s: %w", c.URL, err)
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		return nil, apiError(resp)
	}
	return resp, nil
}

// call makes a request and decodes the data of forgerun's JSON envelope.
func (c *Client) call(ctx context.Context, method, path, contentType string, body io.Reader, out any) error {
	resp, err := c.do(ctx, method, path, contentType, body, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var env struct{ Data json.RawMessage }
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return fmt.Errorf("reading forgerun's answer: %w", err)
	}
	return json.Unmarshal(env.Data, out)
}

// apiError is forgerun's own error message for a failed request.
func apiError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var env struct {
		Error struct{ Message string }
	}
	if json.Unmarshal(b, &env) == nil && env.Error.Message != "" {
		return fmt.Errorf("forgerun: %s (HTTP %d)", env.Error.Message, resp.StatusCode)
	}
	return fmt.Errorf("forgerun answered HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
}
