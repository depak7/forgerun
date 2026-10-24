package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/depak7/forgerun/internal/forge"
	"github.com/depak7/forgerun/internal/publish"
	"github.com/depak7/forgerun/internal/store"
	"github.com/depak7/forgerun/internal/task"
	"github.com/depak7/forgerun/internal/worker"
)

// Client is a worker's side: the queue its pool takes turns from, the sink
// its runner streams logs to, and the heartbeat that reports to the
// orchestrator and carries out its commands.
type Client struct {
	URL     string // the orchestrator, e.g. https://forgerun.example.com
	Token   string
	Name    string // this worker's name in the orchestrator's settings
	Sandbox string // its sandbox backend, reported in heartbeats
	Log     *slog.Logger

	http *http.Client

	mu     sync.Mutex
	tokens map[string]string // task ID → GitHub token to fetch its repo with
}

var _ worker.Queue = (*Client)(nil)

func (c *Client) client() *http.Client {
	if c.http == nil {
		c.http = &http.Client{Timeout: claimWait + 30*time.Second}
	}
	return c.http
}

func (c *Client) do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.URL, "/")+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := c.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("reaching the orchestrator: %w", err)
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("orchestrator %s %s: HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return resp, nil
}

func (c *Client) postJSON(ctx context.Context, path string, in, out any) (int, error) {
	b, err := json.Marshal(in)
	if err != nil {
		return 0, err
	}
	resp, err := c.do(ctx, http.MethodPost, path, bytes.NewReader(b))
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if out != nil && resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp.StatusCode, err
		}
	}
	return resp.StatusCode, nil
}

var (
	// ErrTokenRejected means the orchestrator refused the worker token.
	ErrTokenRejected = errors.New("the orchestrator rejected the worker token")
	// ErrOutdated means the orchestrator predates something this worker uses.
	ErrOutdated = errors.New("the orchestrator is older than this worker")
)

// Hello checks that the orchestrator is reachable and accepts the worker
// token, without registering this worker or taking any work.
func (c *Client) Hello(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.URL, "/")+pathHello, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := c.client().Do(req)
	if err != nil {
		return fmt.Errorf("reaching the orchestrator: %w", err)
	}
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNoContent:
		return nil
	case http.StatusUnauthorized:
		return ErrTokenRejected
	case http.StatusNotFound:
		return ErrOutdated
	default:
		return fmt.Errorf("the orchestrator answered %s", resp.Status)
	}
}

// Claim waits for a turn from the orchestrator.
func (c *Client) Claim(ctx context.Context) (store.Record, bool, error) {
	var a Assignment
	code, err := c.postJSON(ctx, pathClaim, ClaimRequest{Worker: c.Name}, &a)
	if err != nil || code == http.StatusNoContent {
		return store.Record{}, false, err
	}
	if a.FetchToken != "" {
		c.mu.Lock()
		if c.tokens == nil {
			c.tokens = map[string]string{}
		}
		c.tokens[a.Task.ID] = a.FetchToken
		c.mu.Unlock()
	}
	return a.Task, true, nil
}

// Finish reports how a turn ended. It retries for a while: the work is
// done and shouldn't be lost because the orchestrator was briefly away.
func (c *Client) Finish(ctx context.Context, id string, o store.Outcome) error {
	c.mu.Lock()
	delete(c.tokens, id)
	c.mu.Unlock()
	var err error
	for wait := time.Second; wait < 2*time.Minute; wait *= 2 {
		if _, err = c.postJSON(context.WithoutCancel(ctx), pathTask+id+"/finish", o, nil); err == nil {
			return nil
		}
		if strings.Contains(err.Error(), "HTTP 409") { // already finished (e.g. failed as lost)
			return err
		}
		c.Log.Warn("reporting a turn failed; retrying", "id", id, "err", err)
		time.Sleep(wait)
	}
	return err
}

func (c *Client) ThreadTasks(ctx context.Context, threadID string) ([]store.Record, error) {
	resp, err := c.do(ctx, http.MethodGet, pathThread+threadID, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var recs []store.Record
	return recs, json.NewDecoder(resp.Body).Decode(&recs)
}

func (c *Client) Diff(ctx context.Context, id string) (string, error) {
	resp, err := c.do(ctx, http.MethodGet, pathTask+id+"/diff", nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

// Fetchers gives a turn of a private repo a fetcher with the token the
// orchestrator sent: the repo is fetched on this host, never by the VM.
func (c *Client) Fetchers(_ context.Context, rec store.Record) (task.Fetcher, error) {
	c.mu.Lock()
	tok := c.tokens[rec.ID]
	c.mu.Unlock()
	if tok == "" {
		return nil, nil
	}
	p := &publish.Publisher{Tokens: forge.NewStaticToken(forge.NewToken(tok)), GitHub: &forge.GitHub{}}
	return p.Fetcher(forge.Token{}), nil
}

// LogSink streams a turn's log to the orchestrator: what's written is sent
// about twice a second, and the rest when it's closed.
func (c *Client) LogSink(id string) io.WriteCloser {
	u := &logUpload{c: c, id: id, done: make(chan struct{}), stopped: make(chan struct{})}
	go u.loop()
	return u
}

type logUpload struct {
	c       *Client
	id      string
	mu      sync.Mutex
	buf     bytes.Buffer
	done    chan struct{}
	stopped chan struct{}
	once    sync.Once
}

func (u *logUpload) Write(p []byte) (int, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.buf.Write(p)
}

func (u *logUpload) loop() {
	defer close(u.stopped)
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			u.flush()
		case <-u.done:
			u.flush()
			return
		}
	}
}

func (u *logUpload) flush() {
	u.mu.Lock()
	chunk := bytes.Clone(u.buf.Bytes())
	u.buf.Reset()
	u.mu.Unlock()
	if len(chunk) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := u.c.do(ctx, http.MethodPost, pathTask+u.id+"/log", bytes.NewReader(chunk))
	if err != nil {
		u.c.Log.Warn("sending a turn's log", "id", u.id, "err", err)
		return
	}
	resp.Body.Close()
}

func (u *logUpload) Close() error {
	u.once.Do(func() { close(u.done) })
	<-u.stopped
	return nil
}

// Heartbeats reports to the orchestrator every few seconds until ctx ends,
// and carries out the commands it sends back. agents are the agents this
// worker has keys for, adapters every agent it can run (models that need no
// key run on any of them).
func (c *Client) Heartbeats(ctx context.Context, pool *worker.Pool, runner *task.Runner, agents, adapters []string, maxSessions int) {
	connected := false
	for {
		hb := Heartbeat{Worker: c.Name, Agents: agents, Adapters: adapters, Sandbox: c.Sandbox, MaxSessions: maxSessions, Sessions: runner.Sessions(), Running: pool.Running()}
		var reply HeartbeatReply
		hctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		_, err := c.postJSON(hctx, pathHeartbeat, hb, &reply)
		cancel()
		switch {
		case err != nil && ctx.Err() == nil:
			if connected {
				c.Log.Warn("lost the orchestrator; retrying", "err", err)
			}
			connected = false
		case err == nil && !connected:
			c.Log.Info("connected to the orchestrator", "url", c.URL, "as", c.Name)
			connected = true
		}
		for _, cmd := range reply.Commands {
			go c.carryOut(ctx, cmd, pool, runner)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(heartbeatGap):
		}
	}
}

func (c *Client) carryOut(ctx context.Context, cmd Command, pool *worker.Pool, runner *task.Runner) {
	var res CommandResult
	switch cmd.Kind {
	case cmdCancel:
		pool.Cancel(cmd.Task)
		return // nobody waits for it
	case cmdEnd:
		res = resultOf(runner.CloseSession(cmd.Thread))
	case cmdSummarize:
		sctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		summary, err := runner.Summarize(sctx, cmd.Thread)
		cancel()
		res = resultOf(err)
		res.Summary = summary
	default:
		res.Error = "unknown command " + cmd.Kind
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if _, err := c.postJSON(rctx, pathResult+cmd.ID+"/result", res, nil); err != nil {
		c.Log.Warn("answering a command", "kind", cmd.Kind, "err", err)
	}
}

func resultOf(err error) CommandResult {
	switch {
	case err == nil:
		return CommandResult{}
	case errors.Is(err, task.ErrNoSession):
		return CommandResult{NoOpen: true}
	case errors.Is(err, task.ErrSessionBusy):
		return CommandResult{Busy: true}
	}
	return CommandResult{Error: err.Error()}
}
