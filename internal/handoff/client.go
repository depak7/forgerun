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
}

// Task is a turn, as the API reports it (the fields handing off needs).
type Task struct {
	ID       string `json:"id"`
	ThreadID string `json:"thread_id"`
	Repo     string `json:"repo"`
	Folder   string `json:"folder"`
	Agent    string `json:"agent"`
	ModelID  string `json:"model_id"`
	Prompt   string `json:"prompt"`
	State    string `json:"state"`
	Error    string `json:"error"`
	HasDiff  bool   `json:"has_diff"`
	Reply    string `json:"reply"`
	Plan     bool   `json:"plan"`
	Asked    bool   `json:"asked"`
	PR       struct {
		URL    string `json:"url"`
		Branch string `json:"branch"`
		Error  string `json:"error"`
	} `json:"pr"`
}

// Start is what a handed-off thread starts with.
type Start struct {
	Prompt string
	Agent  string // "" = the server's default
	Model  string // "" = the agent's default
	Effort string
	Plan   bool // plan first: the agent proposes and waits
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

// StartThread starts a thread on an uploaded folder.
func (c *Client) StartThread(ctx context.Context, folder string, s Start) (Task, error) {
	body, _ := json.Marshal(map[string]any{"folder": folder, "prompt": s.Prompt, "agent": s.Agent, "model": s.Model, "effort": s.Effort, "plan": s.Plan})
	var t Task
	err := c.call(ctx, http.MethodPost, "/api/tasks", "application/json", bytes.NewReader(body), &t)
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
	resp, err := c.do(ctx, http.MethodGet, "/api/tasks/"+url.PathEscape(id)+"/diff", "", nil)
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

func (c *Client) do(ctx context.Context, method, path, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.URL, "/")+path, body)
	if err != nil {
		return nil, err
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
	resp, err := c.do(ctx, method, path, contentType, body)
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
