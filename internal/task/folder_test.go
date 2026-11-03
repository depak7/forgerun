package task

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/depak7/forgerun/internal/sandbox"
)

// hostSandbox runs commands on this machine, standing in for a VM to test
// shell scripts that only need git, sh and tar.
type hostSandbox struct{ sandbox.Sandbox }

func (hostSandbox) Run(ctx context.Context, c sandbox.Cmd) (string, error) {
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	cmd.Dir = c.Dir
	cmd.Env = os.Environ()
	for k, v := range c.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.Output()
	if ee, ok := err.(*exec.ExitError); ok {
		return string(out), &sandbox.ExitError{Code: ee.ExitCode(), Stderr: string(ee.Stderr)}
	}
	return string(out), err
}

func (hostSandbox) WriteFile(_ context.Context, path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}

// tarball packs dir as a gzipped tar, as a folder upload is.
func tarball(t *testing.T, dir string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	err := filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || p == dir {
			return err
		}
		h, err := tar.FileInfoHeader(fi, "")
		if err != nil {
			return err
		}
		h.Name, _ = filepath.Rel(dir, p)
		if err := tw.WriteHeader(h); err != nil || !fi.Mode().IsRegular() {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	tw.Close()
	zw.Close()
	return buf.Bytes()
}

func hostRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := hostSandbox{}.Run(context.Background(), sandbox.Cmd{Name: args[0], Args: args[1:], Dir: dir})
	if err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	return strings.TrimSpace(out)
}

// unpackUpload unpacks data into a new directory and returns it and its HEAD.
func unpackUpload(t *testing.T, data []byte) (dir, head string) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "repo")
	load := func(context.Context) ([]byte, error) { return data, nil }
	if _, err := unpackFolder(context.Background(), hostSandbox{}, load, dir); err != nil {
		t.Fatal(err)
	}
	return dir, hostRun(t, dir, "git", "rev-parse", "HEAD")
}

// A repo's uncommitted changes become one commit on top of its own, so a
// turn's diff holds only the agent's work; unpacking the same upload again
// gives the same commit, which rebuilding a thread relies on.
func TestUnpackRepoFolder(t *testing.T) {
	src := t.TempDir()
	hostRun(t, src, "git", "init", "-q")
	os.WriteFile(filepath.Join(src, "app.go"), []byte("package app\n"), 0o644)
	os.WriteFile(filepath.Join(src, ".gitignore"), []byte("build/\n"), 0o644)
	hostRun(t, src, "git", "add", "-A")
	hostRun(t, src, "git", "-c", "user.name=u", "-c", "user.email=u@x", "commit", "-q", "-m", "mine")
	mine := hostRun(t, src, "git", "rev-parse", "HEAD")
	os.WriteFile(filepath.Join(src, "app.go"), []byte("package app\n\n// work in progress\n"), 0o644)
	os.WriteFile(filepath.Join(src, "new.go"), []byte("package app\n"), 0o644)
	os.MkdirAll(filepath.Join(src, "build"), 0o755)
	os.WriteFile(filepath.Join(src, "build", "out"), []byte("x"), 0o644)
	data := tarball(t, src)

	dir, head := unpackUpload(t, data)
	if parent := hostRun(t, dir, "git", "rev-parse", "HEAD~1"); parent != mine {
		t.Errorf("the handoff commit's parent is %s, want the folder's own %s", parent, mine)
	}
	if got := hostRun(t, dir, "git", "show", "--name-only", "--format=", "HEAD"); got != "app.go\nnew.go" {
		t.Errorf("the handoff commit has %q, want the uncommitted changes only (not ignored build/)", got)
	}
	if st := hostRun(t, dir, "git", "status", "--porcelain"); st != "" {
		t.Errorf("not clean after unpacking: %q", st)
	}
	if _, again := unpackUpload(t, data); again != head {
		t.Errorf("unpacking again gave %s, want %s", again, head)
	}
	// The work at handoff, as a patch on the folder's own commit.
	load := func(context.Context) ([]byte, error) { return data, nil }
	handoff, err := unpackFolder(context.Background(), hostSandbox{}, load, filepath.Join(t.TempDir(), "r"))
	if err != nil || !strings.Contains(handoff, "+// work in progress") || !strings.Contains(handoff, "new file mode") || strings.Contains(handoff, "build/") {
		t.Errorf("handoff diff (%v):\n%s", err, handoff)
	}
}

// A repo with nothing uncommitted starts from its own commit.
func TestUnpackCleanRepoFolder(t *testing.T) {
	src := t.TempDir()
	hostRun(t, src, "git", "init", "-q")
	os.WriteFile(filepath.Join(src, "a"), []byte("a\n"), 0o644)
	hostRun(t, src, "git", "add", "-A")
	hostRun(t, src, "git", "-c", "user.name=u", "-c", "user.email=u@x", "commit", "-q", "-m", "mine")
	if _, head := unpackUpload(t, tarball(t, src)); head != hostRun(t, src, "git", "rev-parse", "HEAD") {
		t.Errorf("HEAD = %s, want the folder's own commit", head)
	}
	load := func(context.Context) ([]byte, error) { return tarball(t, src), nil }
	if handoff, err := unpackFolder(context.Background(), hostSandbox{}, load, filepath.Join(t.TempDir(), "r")); err != nil || handoff != "" {
		t.Errorf("nothing uncommitted: handoff %q, %v", handoff, err)
	}
}

// A plain folder (or one file) becomes a new repo with one commit.
func TestUnpackPlainFolder(t *testing.T) {
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "notes.md"), []byte("# notes\n"), 0o644)
	data := tarball(t, src)
	dir, head := unpackUpload(t, data)
	if got := hostRun(t, dir, "git", "log", "--format=%s"); got != "The folder as handed off" {
		t.Errorf("log = %q", got)
	}
	if got := hostRun(t, dir, "git", "ls-files"); got != "notes.md" {
		t.Errorf("files = %q", got)
	}
	if _, again := unpackUpload(t, data); again != head {
		t.Errorf("unpacking again gave %s, want %s", again, head)
	}
}
