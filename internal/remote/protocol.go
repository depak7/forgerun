// Package remote lets workers run on other machines than the orchestrator.
// A worker dials out to the orchestrator over HTTPS (nothing to open on the
// worker's side) and long-polls for turns; it streams each turn's log up,
// reports how it ended, and sends a heartbeat every few seconds that the
// orchestrator answers with commands (cancel, end a session, summarize).
//
// Both ends are the same forgerun binary, so the messages are plain JSON of
// forgerun's own types.
package remote

import (
	"time"

	"github.com/depak7/forgerun/internal/store"
	"github.com/depak7/forgerun/internal/task"
)

// Paths, under the orchestrator's address.
const (
	pathHeartbeat = "/api/worker/heartbeat"
	pathClaim     = "/api/worker/claim"
	pathThread    = "/api/worker/threads/"  // {id}
	pathTask      = "/api/worker/tasks/"    // {id}/diff, {id}/log, {id}/finish
	pathResult    = "/api/worker/commands/" // {id}/result
	claimWait     = 25 * time.Second        // how long a claim waits for work
	heartbeatGap  = 2 * time.Second         // how often a worker reports
)

// workerLost: no heartbeat for this long and a worker is gone; a running
// turn no worker reports for this long has failed. (A variable for tests.)
var workerLost = 30 * time.Second

// Heartbeat is a worker's report of itself.
type Heartbeat struct {
	Worker      string             `json:"worker"`   // its name, e.g. the host name
	Agents      []string           `json:"agents"`   // agents it has keys for
	Adapters    []string           `json:"adapters"` // every agent it can run, key or not
	MaxSessions int                `json:"max_sessions"`
	Sessions    []task.OpenSession `json:"sessions"`
	Running     []string           `json:"running"` // task IDs
}

// Command is something the orchestrator asks a worker to do.
type Command struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"` // "cancel", "end" or "summarize"
	Task   string `json:"task,omitempty"`
	Thread string `json:"thread,omitempty"`
}

const (
	cmdCancel    = "cancel"
	cmdEnd       = "end"
	cmdSummarize = "summarize"
)

// CommandResult answers a command that waits for one (end, summarize).
type CommandResult struct {
	Summary string `json:"summary,omitempty"`
	Error   string `json:"error,omitempty"`
	NoOpen  bool   `json:"no_session,omitempty"` // the thread had no open session
	Busy    bool   `json:"busy,omitempty"`       // the session was in use
}

// HeartbeatReply carries commands back.
type HeartbeatReply struct {
	Commands []Command `json:"commands"`
}

// ClaimRequest asks for a turn.
type ClaimRequest struct {
	Worker string `json:"worker"`
}

// Assignment is a turn for a worker.
type Assignment struct {
	Task store.Record `json:"task"`
	// FetchToken, for a private repo, is the GitHub token to fetch it with
	// on the worker's host (never in the VM). "" = the VM clones it.
	FetchToken string `json:"fetch_token,omitempty"`
}
