package task

import (
	"context"
	"fmt"
	"strings"

	"github.com/depak7/forgerun/internal/sandbox"
)

// Fetcher gets a repo's code on the host, for repos a VM can't clone by
// itself (private ones). The VM is handed a plain copy, so it never holds
// a GitHub credential: an agent can't use the user's GitHub access at all.
type Fetcher interface {
	// Fetch returns a gzipped tar of a shallow checkout of repo at ref (a
	// commit, or "" for the latest of the default branch), with its .git,
	// and the default branch's name.
	Fetch(ctx context.Context, repo, ref string) (tarball []byte, defaultBranch string, err error)
}

const (
	srcTarball = "/tmp/forgerun-src.tgz"
	srcDir     = "/tmp/forgerun-src"
)

// unpack puts a fetched copy of the repo at srcDir in the VM, owned by the
// VM's user (git refuses repositories owned by someone else).
func unpack(ctx context.Context, sb *sandbox.Sandbox, f Fetcher, repo, ref string) (defaultBranch string, err error) {
	tarball, branch, err := f.Fetch(ctx, repo, ref)
	if err != nil {
		return "", err
	}
	if err := sb.WriteFile(ctx, srcTarball, tarball); err != nil {
		return "", fmt.Errorf("copying the repo into the VM: %w", err)
	}
	script := `set -e; rm -rf ` + srcDir + `; mkdir -p ` + srcDir + `; tar --no-same-owner -xzf ` + srcTarball + ` -C ` + srcDir + `; rm -f ` + srcTarball
	if _, err := sb.Run(ctx, sandbox.Cmd{Name: "sh", Args: []string{"-c", script}}); err != nil {
		return "", fmt.Errorf("unpacking the repo in the VM: %w", err)
	}
	return branch, nil
}

// HostClone is the first checkout of a repo fetched on the host: it
// becomes dir, with origin pointing at the repo (without credentials) and
// origin/HEAD at its default branch, as a clone would leave it.
func HostClone(ctx context.Context, sb *sandbox.Sandbox, f Fetcher, repo, dir string) error {
	branch, err := unpack(ctx, sb, f, repo, "")
	if err != nil {
		return err
	}
	script := `set -e; mkdir -p "$(dirname "$1")"; mv ` + srcDir + ` "$1"; cd "$1"
git remote set-url origin "$2"
git update-ref "refs/remotes/origin/$3" HEAD
git symbolic-ref refs/remotes/origin/HEAD "refs/remotes/origin/$3"
git checkout -q -B "$3" HEAD`
	if _, err := sb.Run(ctx, sandbox.Cmd{Name: "sh", Args: []string{"-c", script, "clone", dir, repo, branch}}); err != nil {
		return fmt.Errorf("setting up the checkout: %w", err)
	}
	return nil
}

// HostFetch brings a commit fetched on the host into the checkout at dir.
// With ref "" it's the latest of the default branch, recorded as
// origin/<branch> and origin/HEAD (what a git fetch from origin would do);
// otherwise the commit ref, which can then be checked out. It returns the
// fetched commit.
func HostFetch(ctx context.Context, sb *sandbox.Sandbox, f Fetcher, repo, ref, dir string) (string, error) {
	branch, err := unpack(ctx, sb, f, repo, ref)
	if err != nil {
		return "", err
	}
	script := `set -e; cd "$1"
git fetch -q --depth=1 ` + srcDir + ` HEAD
if [ -z "$2" ]; then
  git update-ref "refs/remotes/origin/$3" FETCH_HEAD
  git symbolic-ref refs/remotes/origin/HEAD "refs/remotes/origin/$3"
fi
rm -rf ` + srcDir + `
git rev-parse FETCH_HEAD`
	out, err := sb.Run(ctx, sandbox.Cmd{Name: "sh", Args: []string{"-c", script, "fetch", dir, ref, branch}})
	if err != nil {
		return "", fmt.Errorf("fetching into the checkout: %w", err)
	}
	return strings.TrimSpace(out), nil
}
