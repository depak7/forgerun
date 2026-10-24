package sandbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/depak7/forgerun/internal/secretproxy"
)

// GVisor is the backend for Linux machines without KVM: each sandbox is a
// Docker container run by gVisor (the runsc runtime), whose own user-space
// kernel stands between the agent and the host. Docker pulls the agent
// images and keeps snapshots as images.
//
// gVisor has no key-swapping proxy of its own, so forgerun's secretproxy
// does it: every sandbox gets its own Docker network, a proxy listener on
// that network's gateway holding only its keys, and random placeholders in
// place of them.
type GVisor struct {
	Proxy   *secretproxy.Proxy
	Runtime string // the Docker runtime; "" = "forgerun-runsc" (see EnsureRuntime)
	Docker  string // the docker command; "" = "docker"
	// StateDir holds the proxy's CA certificate, mounted into sandboxes.
	StateDir string
}

var _ Backend = (*GVisor)(nil)

// NewGVisor makes the gVisor backend, with a fresh key proxy whose CA
// certificate is written to stateDir.
func NewGVisor(stateDir string) (*GVisor, error) {
	p, err := secretproxy.New()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return nil, err
	}
	g := &GVisor{Proxy: p, StateDir: stateDir}
	if err := os.WriteFile(g.caPath(), p.CAPEM(), 0o644); err != nil {
		return nil, err
	}
	return g, nil
}

func (g *GVisor) Name() string { return "gvisor" }

// Labels mark what forgerun made, so leftovers and snapshots can be found.
const (
	labelSandbox  = "forgerun.sandbox"  // on containers and networks: the sandbox's name
	labelSnapshot = "forgerun.snapshot" // on snapshot images: their group
	caInSandbox   = "/etc/forgerun/ca.pem"
	snapshotRepo  = "forgerun-snapshot"
	// maxLayers: Docker refuses images with more than 127 layers. A snapshot
	// of a restored sandbox adds one, so past this many it's flattened.
	maxLayers = 60
)

func (g *GVisor) caPath() string { return filepath.Join(g.StateDir, "sandbox-ca.pem") }

func (g *GVisor) docker() string {
	if g.Docker != "" {
		return g.Docker
	}
	return "docker"
}

// defaultRuntime is forgerun's own runsc entry in Docker. It runs with
// --overlay2=none: by default gVisor keeps a container's file changes in its
// own overlay, where docker commit can't see them, so snapshots would lose
// them. A separate entry leaves any other runsc set-up on the machine alone.
const defaultRuntime = "forgerun-runsc"

// RuntimeSetup is how to register forgerun's runsc runtime with Docker.
const RuntimeSetup = "sudo runsc install --runtime=" + defaultRuntime + " -- --overlay2=none && sudo systemctl restart docker"

func (g *GVisor) runtime() string {
	if g.Runtime != "" {
		return g.Runtime
	}
	return defaultRuntime
}

// EnsureRuntime checks that Docker is running and knows forgerun's runsc
// runtime.
func (g *GVisor) EnsureRuntime(ctx context.Context) error {
	out, err := g.run(ctx, nil, "info", "--format", "{{json .Runtimes}}")
	if err != nil {
		return fmt.Errorf("Docker isn't running or can't be reached (%w); gVisor sandboxes need it", err)
	}
	if !strings.Contains(out, `"`+g.runtime()+`"`) {
		return fmt.Errorf("Docker has no %q runtime: install gVisor, then %s", g.runtime(), RuntimeSetup)
	}
	return nil
}

// gvSandbox is a running gVisor sandbox.
type gvSandbox struct {
	g       *GVisor
	name    string
	network string
	proxy   *secretproxy.Listener
	once    sync.Once
}

var _ Sandbox = (*gvSandbox)(nil)

func (g *GVisor) New(ctx context.Context, name string, cfg Config) (Sandbox, error) {
	return g.start(ctx, name, cfg.Image, cfg)
}

func (g *GVisor) Restore(ctx context.Context, ref, name string, cfg Config) (Sandbox, error) {
	return g.start(ctx, name, ref, cfg)
}

// start makes the sandbox's network and proxy listener, then the container.
func (g *GVisor) start(ctx context.Context, name, image string, cfg Config) (Sandbox, error) {
	if g.Proxy == nil {
		return nil, errors.New("gvisor: no key proxy (use NewGVisor)")
	}
	sb := &gvSandbox{g: g, name: name, network: "forgerun-net-" + name}
	fail := func(err error) (Sandbox, error) {
		sb.Destroy()
		return nil, err
	}
	if _, err := g.run(ctx, nil, "network", "create", "--driver", "bridge", "--label", labelSandbox+"="+name, sb.network); err != nil {
		return nil, fmt.Errorf("creating the network for %s: %w", name, err)
	}
	gw, err := g.run(ctx, nil, "network", "inspect", "-f", "{{range .IPAM.Config}}{{.Gateway}}{{end}}", sb.network)
	if gw = strings.TrimSpace(gw); err != nil || gw == "" {
		return fail(fmt.Errorf("finding the gateway of %s's network: %v", name, err))
	}

	var secrets []secretproxy.Secret
	env := map[string]string{}
	for _, s := range cfg.Secrets {
		ph := secretproxy.Placeholder()
		secrets = append(secrets, secretproxy.Secret{Placeholder: ph, Value: s.Value, Hosts: s.AllowHosts})
		env[s.EnvVar] = ph // the sandbox only ever sees the placeholder
	}
	if sb.proxy, err = g.Proxy.Listen(gw+":0", secrets); err != nil {
		return fail(fmt.Errorf("starting the key proxy for %s: %w", name, err))
	}
	proxyURL := "http://" + sb.proxy.Addr().String()
	for k, v := range map[string]string{
		"HTTPS_PROXY": proxyURL, "HTTP_PROXY": proxyURL, "https_proxy": proxyURL, "http_proxy": proxyURL,
		"NO_PROXY": "localhost,127.0.0.1", "no_proxy": "localhost,127.0.0.1",
		"NODE_EXTRA_CA_CERTS": caInSandbox, // Node and Bun add it to their own roots
		"NODE_USE_ENV_PROXY":  "1",         // Node's fetch honours HTTPS_PROXY only with this
	} {
		env[k] = v
	}

	// The container's main process is its lifetime: when it ends, the
	// container stops, even if forgerun crashed and never destroyed it.
	life := "infinity"
	if cfg.MaxLifetime > 0 {
		life = strconv.Itoa(int(cfg.MaxLifetime.Seconds()))
	}
	args := []string{"run", "-d", "--rm", "--name", name, "--runtime", g.runtime(),
		"--network", sb.network, "--label", labelSandbox + "=" + name,
		"--security-opt", "no-new-privileges", "--pids-limit", "4096",
		"-v", g.caPath() + ":" + caInSandbox + ":ro"}
	if cfg.CPUs > 0 {
		args = append(args, "--cpus", strconv.Itoa(int(cfg.CPUs)))
	}
	if cfg.MemoryMiB > 0 {
		args = append(args, "--memory", strconv.Itoa(int(cfg.MemoryMiB))+"m")
	}
	for _, k := range sortedKeys(env) {
		args = append(args, "-e", k+"="+env[k])
	}
	args = append(args, "--entrypoint", "sleep", image, life)
	if _, err := g.run(ctx, nil, args...); err != nil {
		return fail(fmt.Errorf("starting sandbox %s (%s): %w", name, image, err))
	}
	// Tools that read only the system's roots (git, curl) trust the CA too.
	if _, err := sb.Run(ctx, Cmd{Name: "sh", Args: []string{"-c", `f=/etc/ssl/certs/ca-certificates.crt; [ -f "$f" ] && ! grep -q "$(sed -n 2p ` + caInSandbox + `)" "$f" && cat ` + caInSandbox + ` >> "$f"; true`}}); err != nil {
		return fail(fmt.Errorf("trusting the key proxy in %s: %w", name, err))
	}
	return sb, nil
}

func (s *gvSandbox) Name() string { return s.name }

func (s *gvSandbox) Run(ctx context.Context, c Cmd) (string, error) {
	var stdout, stderr bytes.Buffer
	code, err := s.exec(ctx, c, &stdout, &stderr, nil)
	if err != nil {
		return "", fmt.Errorf("%s: %w", c, err)
	}
	if code != 0 {
		return "", &ExitError{Cmd: c, Code: code, Stderr: tail(stderr.String(), 2000)}
	}
	return stdout.String(), nil
}

func (s *gvSandbox) Stream(ctx context.Context, c Cmd, w io.Writer) error {
	sw := &syncWriter{w: w}
	code, err := s.exec(ctx, c, sw, sw, nil)
	if err != nil {
		return fmt.Errorf("%s: %w", c, err)
	}
	if code != 0 {
		return &ExitError{Cmd: c, Code: code}
	}
	return nil
}

func (s *gvSandbox) WriteFile(ctx context.Context, path string, data []byte) error {
	var stderr bytes.Buffer
	c := Cmd{Name: "sh", Args: []string{"-c", `mkdir -p "$(dirname "$1")" && cat > "$1"`, "sh", path}}
	code, err := s.exec(ctx, c, io.Discard, &stderr, bytes.NewReader(data))
	if err == nil && code != 0 {
		err = errors.New(tail(stderr.String(), 500))
	}
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// execMark is the variable that marks a command's processes: everything it
// starts inherits it, so when ctx ends they can all be found and stopped
// inside the sandbox (killing the docker client would leave them running).
const execMark = "FORGERUN_EXEC"

// exec runs c inside the sandbox and returns its exit code.
func (s *gvSandbox) exec(ctx context.Context, c Cmd, stdout, stderr io.Writer, stdin io.Reader) (int, error) {
	mark := randomHex(8)
	args := []string{"exec", "-e", execMark + "=" + mark}
	if stdin != nil {
		args = append(args, "-i")
	}
	if c.Dir != "" {
		args = append(args, "-w", c.Dir)
	}
	for _, k := range sortedKeys(c.Env) {
		args = append(args, "-e", k+"="+c.Env[k])
	}
	args = append(args, s.name, c.Name)
	args = append(args, c.Args...)

	cmd := exec.Command(s.g.docker(), args...)
	cmd.Stdout, cmd.Stderr, cmd.Stdin = stdout, stderr, stdin
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode(), nil
		}
		return 0, err
	case <-ctx.Done():
		s.stopMarked(mark)
		<-done
		return 0, ctx.Err()
	}
}

// stopMarked stops every process in the sandbox carrying mark: TERM, then
// KILL whatever is left after 5 seconds. It reads /proc, so it needs no
// tools beyond sh, tr and grep.
func (s *gvSandbox) stopMarked(mark string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	script := `pids() { for p in /proc/[0-9]*; do tr '\0' '\n' < $p/environ 2>/dev/null | grep -qx "$1" && echo ${p#/proc/}; done; }
m=` + execMark + `=` + mark + `
kill -TERM $(pids $m) 2>/dev/null
for i in 1 2 3 4 5; do [ -z "$(pids $m)" ] && exit 0; sleep 1; done
kill -KILL $(pids $m) 2>/dev/null; true`
	s.g.run(ctx, nil, "exec", s.name, "sh", "-c", script)
}

// Destroy removes the container, its network and its proxy listener.
func (s *gvSandbox) Destroy() error {
	var err error
	s.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, rmErr := s.g.run(ctx, nil, "rm", "-f", s.name); rmErr != nil && !strings.Contains(rmErr.Error(), "No such container") {
			err = rmErr
		}
		if s.proxy != nil {
			s.proxy.Close()
		}
		s.g.run(ctx, nil, "network", "rm", s.network)
	})
	return err
}

// TakeSnapshot commits the container as an image. When its layers stack up
// (each snapshot of a restored sandbox adds one), it's flattened into one.
func (s *gvSandbox) TakeSnapshot(ctx context.Context, group, name string) (Snapshot, error) {
	ref := snapshotRef(group, name)
	layers, _ := s.g.run(ctx, nil, "image", "inspect", "-f", "{{len .RootFS.Layers}}", s.imageOf(ctx))
	n, _ := strconv.Atoi(strings.TrimSpace(layers))
	var err error
	if n >= maxLayers {
		err = s.flatten(ctx, ref, group)
	} else {
		_, err = s.g.run(ctx, nil, "commit", "--pause=false", "--change", "LABEL "+labelSnapshot+"="+group, s.name, ref)
	}
	if err != nil {
		return Snapshot{}, fmt.Errorf("taking snapshot %s of %s: %w", name, s.name, err)
	}
	// The newest layer is what this snapshot adds on top of its base.
	size, _ := s.g.run(ctx, nil, "history", "--no-trunc", "--human=false", "--format", "{{.Size}}", ref)
	b, _ := strconv.ParseInt(strings.TrimSpace(strings.SplitN(size, "\n", 2)[0]), 10, 64)
	return Snapshot{Ref: ref, SizeBytes: b}, nil
}

// imageOf is the image the container was started from.
func (s *gvSandbox) imageOf(ctx context.Context) string {
	out, _ := s.g.run(ctx, nil, "inspect", "-f", "{{.Image}}", s.name)
	return strings.TrimSpace(out)
}

// flatten saves the container's file system as a one-layer image, with
// the environment and working directory of the image it came from.
func (s *gvSandbox) flatten(ctx context.Context, ref, group string) error {
	img := s.imageOf(ctx)
	cfg, err := s.g.run(ctx, nil, "image", "inspect", "-f", "{{range .Config.Env}}{{println .}}{{end}}", img)
	if err != nil {
		return err
	}
	importArgs := []string{"import", "--change", "LABEL " + labelSnapshot + "=" + group}
	for _, e := range strings.Split(strings.TrimSpace(cfg), "\n") {
		if e != "" && !strings.Contains(e, "forgerun-secret-") {
			importArgs = append(importArgs, "--change", "ENV "+e)
		}
	}
	importArgs = append(importArgs, "-", ref)
	export := exec.CommandContext(ctx, s.g.docker(), "export", s.name)
	imp := exec.CommandContext(ctx, s.g.docker(), importArgs...)
	pipe, err := export.StdoutPipe()
	if err != nil {
		return err
	}
	imp.Stdin = pipe
	var stderr bytes.Buffer
	imp.Stderr = &stderr
	if err := export.Start(); err != nil {
		return err
	}
	if err := imp.Run(); err != nil {
		export.Wait()
		return fmt.Errorf("flattening: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return export.Wait()
}

func (s *gvSandbox) Compact(context.Context) (int, int, error) { return 0, 0, nil } // see TakeSnapshot

// snapshotRef is a snapshot's image name: forgerun-snapshot:<group>-<name>,
// lower case as Docker requires.
func snapshotRef(group, name string) string {
	tag := strings.ToLower(group + "-" + name)
	tag = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '-'
	}, tag)
	if len(tag) > 128 {
		tag = tag[len(tag)-128:]
	}
	return snapshotRepo + ":" + tag
}

func (g *GVisor) DeleteSnapshot(ctx context.Context, ref string) error {
	_, err := g.run(ctx, nil, "image", "rm", ref)
	if err != nil && !strings.Contains(err.Error(), "No such image") {
		return fmt.Errorf("deleting snapshot %s: %w", ref, err)
	}
	return nil
}

func (g *GVisor) SnapshotGone(ref string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err := g.run(ctx, nil, "image", "inspect", "-f", "{{.Id}}", ref)
	return err != nil && strings.Contains(err.Error(), "No such image")
}

func (g *GVisor) ListSnapshots(ctx context.Context) ([]SnapshotInfo, error) {
	out, err := g.run(ctx, nil, "images", "--filter", "label="+labelSnapshot, "--format", "{{.Repository}}:{{.Tag}}")
	if err != nil {
		return nil, fmt.Errorf("listing snapshots: %w", err)
	}
	var infos []SnapshotInfo
	for _, ref := range strings.Fields(out) {
		line, err := g.run(ctx, nil, "image", "inspect", "-f", `{{index .Config.Labels "`+labelSnapshot+`"}}|{{.Created}}`, ref)
		if err != nil {
			continue
		}
		group, created, _ := strings.Cut(strings.TrimSpace(line), "|")
		t, _ := time.Parse(time.RFC3339Nano, created)
		infos = append(infos, SnapshotInfo{Ref: ref, Group: group, CreatedAt: t})
	}
	return infos, nil
}

// RemoveLeftovers removes the containers and networks of sandboxes whose
// names start with prefix.
func (g *GVisor) RemoveLeftovers(ctx context.Context, prefix string) (int, error) {
	if prefix == "" {
		return 0, errors.New("RemoveLeftovers needs a prefix")
	}
	out, err := g.run(ctx, nil, "ps", "-a", "--filter", "label="+labelSandbox, "--format", "{{.Names}}")
	if err != nil {
		return 0, fmt.Errorf("listing sandboxes: %w", err)
	}
	n := 0
	for _, name := range strings.Fields(out) {
		if strings.HasPrefix(name, prefix) {
			if _, err := g.run(ctx, nil, "rm", "-f", name); err != nil {
				return n, fmt.Errorf("removing leftover sandbox %s: %w", name, err)
			}
			n++
		}
	}
	nets, _ := g.run(ctx, nil, "network", "ls", "--filter", "label="+labelSandbox, "--format", "{{.Name}}")
	for _, net := range strings.Fields(nets) {
		if strings.HasPrefix(net, "forgerun-net-"+prefix) {
			g.run(ctx, nil, "network", "rm", net)
		}
	}
	return n, nil
}

// run runs a docker command and returns its stdout; a failure's error
// carries docker's own message.
func (g *GVisor) run(ctx context.Context, stdin io.Reader, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, g.docker(), args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr, cmd.Stdin = &stdout, &stderr, stdin
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return stdout.String(), fmt.Errorf("docker %s: %s", args[0], msg)
	}
	return stdout.String(), nil
}

// syncWriter lets stdout and stderr share one writer safely.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

func sortedKeys(m map[string]string) []string { return slices.Sorted(maps.Keys(m)) }

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}
