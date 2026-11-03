package task

import (
	"context"
	"fmt"

	"github.com/depak7/forgerun/internal/sandbox"
)

// folderDate is the date of the commit an unpacked folder is recorded in.
// It's fixed so the commit is too: unpacking the same folder again, when a
// closed session is rebuilt, gives the commit the thread started from.
const folderDate = "2000-01-01T00:00:00Z"

// unpackFolder makes dir a git checkout of an uploaded folder (a gzipped
// tar): the folder's own repo if it has one, else a new one. Whatever isn't
// committed yet (all of a plain folder) is committed as the folder's state
// at handoff, so the agent's changes are all a turn's diff holds. For a
// repo, it returns that commit's changes (git diff --binary): the work at
// handoff, which pushing puts on the repo's own commit first.
//
// Everything runs in the VM: the folder's .git can configure commands
// (hooks, filters) that must never run on the host.
func unpackFolder(ctx context.Context, sb sandbox.Sandbox, load func(context.Context) ([]byte, error), dir string) (handoff string, err error) {
	tarball, err := load(ctx)
	if err != nil {
		return "", fmt.Errorf("loading the folder: %w", err)
	}
	if err := sb.WriteFile(ctx, srcTarball, tarball); err != nil {
		return "", fmt.Errorf("copying the folder into the VM: %w", err)
	}
	script := `set -e
rm -rf "$1"; mkdir -p "$1"
tar --no-same-owner -xzf ` + srcTarball + ` -C "$1"; rm -f ` + srcTarball + `
cd "$1"
[ -e .git ] || git init -q
git add -A
if git rev-parse -q --verify HEAD >/dev/null; then
  git diff --cached --quiet && exit 0
  parent=yes
fi
git -c user.name=forgerun -c user.email=forgerun@localhost -c commit.gpgsign=false -c core.hooksPath=/dev/null \
  commit -q --no-verify --allow-empty -m "The folder as handed off"
[ -z "$parent" ] || git diff --binary HEAD~1 HEAD`
	handoff, err = sb.Run(ctx, sandbox.Cmd{Name: "sh", Args: []string{"-c", script, "unpack", dir},
		Env: map[string]string{"GIT_AUTHOR_DATE": folderDate, "GIT_COMMITTER_DATE": folderDate}})
	if err != nil {
		return "", fmt.Errorf("unpacking the folder in the VM: %w", err)
	}
	return handoff, nil
}
