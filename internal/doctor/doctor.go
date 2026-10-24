// Package doctor checks whether a machine is set up to run forgerun: as a
// worker (it runs the microVMs), as serve (everything on one machine) or as
// server (the orchestrator alone). It checks setup only; agents' own quirks
// are handled by their adapters.
//
// Every check reads the machine through Env, so tests can fake any setup.
package doctor

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"strings"
	"time"
)

// Role is what the machine is for.
type Role string

const (
	RoleWorker Role = "worker" // runs the microVMs for an orchestrator elsewhere
	RoleServe  Role = "serve"  // everything on one machine
	RoleServer Role = "server" // the orchestrator alone: no VMs
)

// Status is how a check came out.
type Status string

const (
	Pass Status = "PASS"
	Warn Status = "WARN" // works, but worth fixing
	Fail Status = "FAIL" // forgerun won't work until it's fixed
)

// Result is one check's outcome. Fix is how to put it right; "" if nothing to do.
type Result struct {
	Name   string `json:"name"`
	Status Status `json:"status"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

// What Env.Hello returns when the orchestrator refuses the worker token, or
// is too old to check it.
var (
	ErrTokenRejected = errors.New("worker token rejected")
	ErrOutdated      = errors.New("the orchestrator is older than this worker")
)

// Agent is an agent's credential, for the keys check.
type Agent struct {
	ID         string
	Credential string // e.g. "CURSOR_API_KEY"; "" if it needs none
}

// Env is everything the checks look at. A nil func skips what needs it.
type Env struct {
	GOOS, GOARCH string
	Getenv       func(string) string
	ReadFile     func(string) ([]byte, error)
	Stat         func(string) (fs.FileInfo, error)
	OpenRW       func(string) error // opens a file read-write and closes it
	LookPath     func(string) (string, error)
	Sysctl       func(string) (string, error) // macOS: sysctl -n <name>
	FreeDisk     func(string) (uint64, error) // bytes free for this user under a path
	Memory       func() (uint64, error)       // total bytes of RAM
	Home         string                       // the user's home directory

	Reach   func(ctx context.Context, url string) error // any HTTP answer = reachable
	Runtime func() (string, error)                      // where the microsandbox runtime is
	MSBHome func() (string, error)                      // microsandbox's data directory
	BootVM  func(ctx context.Context) error             // boots a small VM, runs a command, removes it

	Hello      func(ctx context.Context, server, token string) error // the worker token check; ErrTokenRejected if refused
	Migrations func(ctx context.Context, dbURL string) (applied, total int, err error)
	GitHub     func(ctx context.Context, token string) (login string, expires time.Time, err error)
	SignIn     func() error // validates the sign-in settings, as serve does at start
	Agents     []Agent
	Now        func() time.Time
}

const gb = 1 << 30

// Run runs the role's checks, in the order they're worth reading. Booting a
// VM comes last and is skipped when virtualization already failed.
func Run(ctx context.Context, role Role, e Env) []Result {
	var out []Result
	add := func(r Result) { out = append(out, r) }
	vms := role == RoleWorker || role == RoleServe
	if vms {
		add(platform(e))
		add(virtualization(e))
		add(runtime(e))
		add(msbDatabase(e))
		add(disk(e))
		add(memory(e))
	}
	add(tools(e))
	if vms {
		add(dockerHub(ctx, e))
	}
	if role == RoleWorker {
		add(orchestrator(ctx, e))
	}
	if role == RoleServe || role == RoleServer {
		add(database(ctx, e))
		add(githubToken(ctx, e))
		add(signIn(e))
	}
	if role == RoleServer {
		add(workerToken(e))
	}
	if vms {
		add(keys(e))
		if failed(out, "virtualization") || failed(out, "platform") {
			add(Result{Name: "cold start", Status: Warn, Detail: "skipped: this machine can't run microVMs yet"})
		} else {
			add(coldStart(ctx, e))
		}
	}
	return out
}

// Failed reports whether any check failed.
func Failed(rs []Result) bool {
	for _, r := range rs {
		if r.Status == Fail {
			return true
		}
	}
	return false
}

func failed(rs []Result, name string) bool {
	for _, r := range rs {
		if r.Name == name && r.Status == Fail {
			return true
		}
	}
	return false
}

func platform(e Env) Result {
	r := Result{Name: "platform", Detail: e.GOOS + "/" + e.GOARCH}
	switch {
	case e.GOOS == "darwin" && e.GOARCH == "arm64":
		r.Status, r.Detail = Pass, "macOS on Apple Silicon"
	case e.GOOS == "darwin":
		r.Status, r.Detail = Fail, "an Intel Mac: microVMs need Apple Silicon"
		r.Fix = "run VMs on an Apple Silicon Mac or a Linux machine with KVM (this Mac can still be the orchestrator: forgerun server)"
	case e.GOOS == "linux" && (e.GOARCH == "amd64" || e.GOARCH == "arm64"):
		r.Status = Pass
	default:
		r.Status, r.Fix = Fail, "microVMs run on macOS (Apple Silicon) or Linux (x86_64, arm64)"
	}
	return r
}

func virtualization(e Env) Result {
	r := Result{Name: "virtualization"}
	if e.GOOS == "darwin" {
		v, err := call1(e.Sysctl, "kern.hv_support")
		if err == nil && strings.TrimSpace(v) == "1" {
			r.Status, r.Detail = Pass, "Hypervisor.framework available"
			return r
		}
		r.Status, r.Detail = Fail, "Hypervisor.framework isn't available"
		r.Fix = "needs macOS 11 or later on Apple Silicon, not inside a VM"
		return r
	}
	if _, err := e.Stat("/dev/kvm"); err == nil {
		if err := e.OpenRW("/dev/kvm"); err != nil {
			r.Status, r.Detail = Fail, "/dev/kvm exists, but your user can't use it"
			r.Fix = "sudo usermod -aG kvm $USER, then log out and back in"
			return r
		}
		r.Status, r.Detail = Pass, "/dev/kvm usable"
		return r
	}
	flags := cpuFlags(e)
	vmx := flags["vmx"] || flags["svm"]
	inVM := flags["hypervisor"]
	r.Status = Fail
	switch {
	case vmx && inVM:
		r.Detail = "this machine is a VM: the CPU offers virtualization, but there's no /dev/kvm"
		r.Fix = "try: sudo modprobe kvm_intel (or kvm_amd). If that fails, the provider's kernel has no KVM: use a VM with nested virtualization or a bare-metal machine"
	case vmx:
		r.Detail = "the CPU supports virtualization, but /dev/kvm is missing"
		r.Fix = "sudo modprobe kvm_intel (Intel) or sudo modprobe kvm_amd (AMD)"
	case inVM:
		r.Detail = "this machine is a VM without nested virtualization"
		r.Fix = "turn on nested virtualization at your provider, or use a bare-metal machine"
	case e.GOARCH == "arm64":
		r.Detail = "/dev/kvm is missing"
		r.Fix = "use a kernel with KVM enabled, on hardware that supports it"
	default:
		r.Detail = "CPU virtualization is off or unsupported"
		r.Fix = "turn on Intel VT-x or AMD-V (SVM) in the BIOS/UEFI settings"
	}
	return r
}

// cpuFlags are the flags in /proc/cpuinfo.
func cpuFlags(e Env) map[string]bool {
	flags := map[string]bool{}
	b, err := call1(e.ReadFile, "/proc/cpuinfo")
	if err != nil {
		return flags
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		name, val, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(name) != "flags" {
			continue
		}
		for f := range strings.FieldsSeq(val) {
			flags[f] = true
		}
		break // every core lists the same flags
	}
	return flags
}

func runtime(e Env) Result {
	r := Result{Name: "microsandbox runtime"}
	if e.Runtime == nil {
		return skipped(r)
	}
	if path, err := e.Runtime(); err == nil {
		r.Status, r.Detail = Pass, path
		return r
	}
	r.Status, r.Detail = Pass, "not installed yet: forgerun installs it on its first start"
	return r
}

// sqliteHeader starts every SQLite database file.
var sqliteHeader = []byte("SQLite format 3\x00")

func msbDatabase(e Env) Result {
	r := Result{Name: "microsandbox database"}
	if e.MSBHome == nil {
		return skipped(r)
	}
	home, err := e.MSBHome()
	if err != nil {
		r.Status, r.Detail = Warn, err.Error()
		return r
	}
	path := home + "/db/msb.db"
	b, err := e.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		r.Status, r.Detail = Pass, "none yet: created on first start"
	case err != nil:
		r.Status, r.Detail = Fail, "can't read "+path+": "+err.Error()
	case !bytes.HasPrefix(b, sqliteHeader):
		r.Status, r.Detail = Fail, path+" is damaged (not an SQLite database)"
		r.Fix = "move it aside: mv " + path + " ~/msb.db.broken (microsandbox makes a new one; saved snapshots are forgotten)"
	default:
		r.Status, r.Detail = Pass, path
	}
	return r
}

func disk(e Env) Result {
	r := Result{Name: "disk"}
	if e.FreeDisk == nil {
		return skipped(r)
	}
	free, err := e.FreeDisk(e.Home)
	if err != nil {
		r.Status, r.Detail = Warn, err.Error()
		return r
	}
	r.Detail = fmt.Sprintf("%.0f GB free in %s", float64(free)/gb, e.Home)
	switch {
	case free < 5*gb:
		r.Status, r.Fix = Fail, "agent images and warm-start snapshots need at least 5 GB; 20 GB or more is comfortable"
	case free < 20*gb:
		r.Status, r.Fix = Warn, "fine to start; snapshots default to a 20 GB budget (lower it with -snapshot-budget)"
	default:
		r.Status = Pass
	}
	return r
}

func memory(e Env) Result {
	r := Result{Name: "memory"}
	if e.Memory == nil {
		return skipped(r)
	}
	total, err := e.Memory()
	if err != nil {
		r.Status, r.Detail = Warn, err.Error()
		return r
	}
	// Each open session's VM gets 2 GB; leave about 2 GB for the host.
	sessions := (int64(total) - 2*gb) / (2 * gb)
	r.Detail = fmt.Sprintf("%.0f GB: room for about %d session(s) at 2 GB each", float64(total)/gb, max(sessions, 0))
	switch {
	case sessions < 1:
		r.Status, r.Fix = Fail, "a session's VM needs 2 GB on top of what the machine itself uses: 4 GB of RAM at least"
	case sessions < 4:
		r.Status, r.Fix = Warn, fmt.Sprintf("start it with -sessions %d so it never opens more than fit", sessions)
	default:
		r.Status = Pass
	}
	return r
}

func tools(e Env) Result {
	r := Result{Name: "git and tar"}
	var missing []string
	for _, t := range []string{"git", "tar"} {
		if _, err := e.LookPath(t); err != nil {
			missing = append(missing, t)
		}
	}
	if len(missing) > 0 {
		r.Status, r.Detail = Fail, strings.Join(missing, " and ")+" not found"
		r.Fix = "install " + strings.Join(missing, " and ") + " (forgerun uses them to fetch repos and push branches)"
		return r
	}
	r.Status, r.Detail = Pass, "found"
	return r
}

func dockerHub(ctx context.Context, e Env) Result {
	r := Result{Name: "image registry"}
	if e.Reach == nil {
		return skipped(r)
	}
	if err := e.Reach(ctx, "https://registry-1.docker.io/v2/"); err != nil {
		r.Status, r.Detail = Fail, "can't reach Docker Hub: "+err.Error()
		r.Fix = "agent images come from Docker Hub: check DNS and that registry-1.docker.io:443 isn't blocked"
		return r
	}
	r.Status, r.Detail = Pass, "Docker Hub reachable (agent images)"
	return r
}

func orchestrator(ctx context.Context, e Env) Result {
	r := Result{Name: "orchestrator", Status: Fail}
	server, token := e.Getenv("FORGERUN_SERVER"), e.Getenv("FORGERUN_WORKER_TOKEN")
	switch {
	case server == "":
		r.Detail, r.Fix = "FORGERUN_SERVER isn't set", "set it to the orchestrator's address, e.g. https://forgerun.example.com"
		return r
	case token == "":
		r.Detail, r.Fix = "FORGERUN_WORKER_TOKEN isn't set", "use the orchestrator's FORGERUN_WORKER_TOKEN"
		return r
	case e.Hello == nil:
		return skipped(r)
	}
	err := e.Hello(ctx, server, token)
	switch {
	case err == nil:
		r.Status, r.Detail = Pass, server+": reachable, worker token accepted"
		if u, perr := url.Parse(server); perr == nil && u.Scheme == "http" && !loopback(u.Hostname()) {
			r.Status, r.Fix = Warn, "use https: over http the worker token and repo tokens travel unencrypted"
		}
	case errors.Is(err, ErrOutdated):
		r.Detail, r.Fix = server+": the orchestrator is older than this worker", "update forgerun on the orchestrator and redeploy it"
	case errors.Is(err, ErrTokenRejected):
		r.Detail, r.Fix = server+": the worker token was rejected", "copy the orchestrator's FORGERUN_WORKER_TOKEN exactly"
	default:
		r.Detail, r.Fix = err.Error(), "check FORGERUN_SERVER, and that the orchestrator is running"
	}
	return r
}

func database(ctx context.Context, e Env) Result {
	r := Result{Name: "database", Status: Fail}
	if e.Migrations == nil {
		return skipped(r)
	}
	dbURL := cmp.Or(e.Getenv("FORGERUN_DATABASE_URL"), e.Getenv("DATABASE_URL"))
	applied, total, err := e.Migrations(ctx, dbURL)
	if err != nil {
		msg, _, _ := strings.Cut(err.Error(), "\n") // pgx lists every address it tried
		r.Detail = "can't connect: " + strings.TrimSuffix(msg, ":")
		r.Fix = "start Postgres (docker compose up -d), or set FORGERUN_DATABASE_URL"
		return r
	}
	switch {
	case applied > total:
		r.Status, r.Detail = Warn, fmt.Sprintf("the database has %d migrations, this forgerun knows %d", applied, total)
		r.Fix = "a newer forgerun used this database: update this one"
	case applied < total:
		r.Status, r.Detail = Pass, fmt.Sprintf("connected; %d of %d migrations applied (the rest run on start)", applied, total)
	default:
		r.Status, r.Detail = Pass, fmt.Sprintf("connected; all %d migrations applied", total)
	}
	return r
}

func githubToken(ctx context.Context, e Env) Result {
	r := Result{Name: "GITHUB_TOKEN", Status: Fail}
	tok := e.Getenv("GITHUB_TOKEN")
	if tok == "" {
		r.Detail, r.Fix = "not set", "forgerun pushes branches with it: a fine-grained token with Contents and Pull requests read/write"
		return r
	}
	if e.GitHub == nil {
		return skipped(r)
	}
	login, expires, err := e.GitHub(ctx, tok)
	if err != nil {
		r.Detail, r.Fix = "GitHub refused it: "+err.Error(), "make a new token (it may have expired or been revoked)"
		return r
	}
	r.Status, r.Detail = Pass, "valid, as "+login
	if !expires.IsZero() {
		left := expires.Sub(e.Now())
		r.Detail += ", expires " + expires.Format(time.DateOnly)
		if left < 14*24*time.Hour {
			r.Status, r.Fix = Warn, fmt.Sprintf("expires in %d days: make a new one before then", int(left.Hours()/24))
		}
	}
	return r
}

func signIn(e Env) Result {
	r := Result{Name: "sign-in"}
	id, public := e.Getenv("GITHUB_OAUTH_CLIENT_ID"), e.Getenv("FORGERUN_PUBLIC_URL")
	if id == "" {
		r.Status, r.Detail = Pass, "off: local mode, every request is the admin"
		if u, err := url.Parse(public); public != "" && err == nil && !loopback(u.Hostname()) {
			r.Status = Warn
			r.Fix = "FORGERUN_PUBLIC_URL is public but sign-in is off: anyone who opens it is admin. Set GITHUB_OAUTH_CLIENT_ID and GITHUB_OAUTH_CLIENT_SECRET"
		}
		return r
	}
	// GitHub Apps' client IDs start with "Iv"; OAuth Apps' with "Ov" (or are hex).
	if strings.HasPrefix(id, "Iv") {
		r.Status, r.Detail = Fail, "GITHUB_OAUTH_CLIENT_ID is a GitHub App's, not an OAuth App's"
		r.Fix = "make an OAuth App (Settings → Developer settings → OAuth Apps): a GitHub App's sign-in tokens expire in 8 hours and only reach repos it's installed on"
		return r
	}
	if e.SignIn != nil {
		if err := e.SignIn(); err != nil {
			r.Status, r.Detail = Fail, err.Error()
			return r
		}
	}
	r.Status, r.Detail = Pass, "GitHub sign-in on, for "+cmp.Or(public, "http://localhost:8080")
	return r
}

func workerToken(e Env) Result {
	r := Result{Name: "worker token"}
	if len(e.Getenv("FORGERUN_WORKER_TOKEN")) < 16 {
		r.Status, r.Detail = Fail, "FORGERUN_WORKER_TOKEN is missing or shorter than 16 characters"
		r.Fix = "set it to a random value (openssl rand -hex 32); workers connect with the same one"
		return r
	}
	r.Status, r.Detail = Pass, "set"
	return r
}

func keys(e Env) Result {
	r := Result{Name: "agent keys"}
	var set, missing []string
	for _, a := range e.Agents {
		if a.Credential == "" {
			continue
		}
		if e.Getenv(a.Credential) != "" {
			set = append(set, a.ID)
		} else {
			missing = append(missing, a.ID+" ("+a.Credential+")")
		}
	}
	switch {
	case len(set) == 0:
		r.Status, r.Detail = Warn, "none set"
		r.Fix = "only models that need no key (OpenCode's free ones) will run; add keys to the env file for the others"
	default:
		r.Status, r.Detail = Pass, "set: "+strings.Join(set, ", ")
		if len(missing) > 0 {
			r.Detail += "; not set: " + strings.Join(missing, ", ")
		}
	}
	return r
}

func coldStart(ctx context.Context, e Env) Result {
	r := Result{Name: "cold start"}
	if e.BootVM == nil {
		r.Status, r.Detail = Pass, "skipped (-quick)"
		return r
	}
	start := e.Now()
	if err := e.BootVM(ctx); err != nil {
		r.Status, r.Detail = Fail, "couldn't boot a microVM: "+err.Error()
		r.Fix = "see the virtualization and microsandbox checks above"
		if strings.Contains(err.Error(), "socket path is too long") {
			r.Fix = "the home folder's path is too long for the VM's sockets: set MSB_HOME to a short folder, e.g. MSB_HOME=/opt/msb"
		}
		return r
	}
	took := e.Now().Sub(start)
	r.Status, r.Detail = Pass, fmt.Sprintf("booted a microVM and ran a command in %.1fs", took.Seconds())
	if took > time.Minute {
		r.Detail += " (the first run also downloads the runtime and a small image)"
	}
	return r
}

func skipped(r Result) Result {
	r.Status, r.Detail = Warn, "not checked"
	return r
}

func call1[T any](f func(string) (T, error), arg string) (T, error) {
	if f == nil {
		var zero T
		return zero, errors.New("unavailable")
	}
	return f(arg)
}

func loopback(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}
