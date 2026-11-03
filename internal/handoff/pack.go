// Package handoff hands work from a local folder to forgerun: it packs the
// folder as forgerun takes it, and talks to forgerun's API. forgerun's MCP
// server (forgerun mcp) uses it, so a local agent can send the rest of its
// work to forgerun.
package handoff

import (
	"archive/tar"
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

// maxPack stops packing a folder far too big for any server, before it's
// all in memory. Servers have their own, smaller limit.
const maxPack = 256 << 20

// Packed is a folder as it's sent to forgerun.
type Packed struct {
	Data []byte // gzipped tar
	Name string // the folder's (or file's) name
	Root string // what was packed: a repo's top folder, for a path inside one
	// For a git repo: the commit it's on, its branch ("" if detached), and
	// its remote's URL as https://github.com/owner/name when the remote is
	// on GitHub ("" otherwise).
	Base, Branch, Repo string
	Files              int      // files sent
	Skipped            []string // secret files left out, relative to Root
}

// Pack packs the folder or file at p. In a git repo it packs the repo's top
// folder: its files but not what .gitignore ignores, and its current
// commit as a one-commit history, so the copy starts where the repo is,
// uncommitted work included. Secret files (.env, keys) that git doesn't
// track are never sent; a plain folder is sent whole but for those.
func Pack(ctx context.Context, p string) (Packed, error) {
	p, err := filepath.Abs(p)
	if err != nil {
		return Packed{}, err
	}
	fi, err := os.Stat(p)
	if err != nil {
		return Packed{}, err
	}
	if !fi.IsDir() {
		pk := Packed{Name: filepath.Base(p), Root: filepath.Dir(p)}
		return pk, pk.write(ctx, "", []string{pk.Name})
	}
	top, err := git(ctx, p, "rev-parse", "--show-toplevel")
	if err != nil {
		pk := Packed{Name: filepath.Base(p), Root: p}
		files, err := plainFiles(p, &pk.Skipped)
		if err != nil {
			return Packed{}, err
		}
		return pk, pk.write(ctx, "", files)
	}
	pk := Packed{Name: filepath.Base(top), Root: top}
	return pk, pk.packRepo(ctx)
}

func (pk *Packed) packRepo(ctx context.Context) error {
	out, err := git(ctx, pk.Root, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return err
	}
	untracked, err := git(ctx, pk.Root, "ls-files", "-z", "--others", "--exclude-standard")
	if err != nil {
		return err
	}
	isUntracked := map[string]bool{}
	for f := range strings.SplitSeq(untracked, "\x00") {
		isUntracked[f] = true
	}
	var files []string
	for f := range strings.SplitSeq(out, "\x00") {
		switch {
		case f == "":
		case isUntracked[f] && secret(path.Base(f)):
			// A tracked one is in the repo's history anyway; an untracked one
			// never left this machine.
			pk.Skipped = append(pk.Skipped, f)
		default:
			files = append(files, f)
		}
	}
	pk.Base, err = git(ctx, pk.Root, "rev-parse", "-q", "--verify", "HEAD")
	if err != nil {
		return pk.write(ctx, "", files) // no commit yet: a plain folder
	}
	pk.Branch, _ = git(ctx, pk.Root, "symbolic-ref", "-q", "--short", "HEAD")
	pk.Repo = remoteRepo(ctx, pk.Root, pk.Branch)
	history, err := pk.history(ctx)
	if err != nil {
		return err
	}
	defer os.RemoveAll(history)
	return pk.write(ctx, history, files)
}

// history makes a new repo holding only the current commit (a shallow
// fetch of it) and returns its folder: the copy's .git, without the
// repo's other history, its config (remotes, credentials) or hooks.
func (pk *Packed) history(ctx context.Context) (string, error) {
	dir, err := os.MkdirTemp("", "forgerun-handoff-")
	if err != nil {
		return "", err
	}
	format, err := git(ctx, pk.Root, "rev-parse", "--show-object-format")
	if err != nil {
		format = "sha1"
	}
	branch := "refs/heads/" + cmp.Or(pk.Branch, "handoff")
	steps := [][]string{
		{"init", "-q", "--object-format=" + format},
		{"fetch", "-q", "--no-tags", "--depth=1", pk.Root, pk.Base},
		{"update-ref", branch, pk.Base},
		{"symbolic-ref", "HEAD", branch},
	}
	for _, args := range steps {
		if _, err := git(ctx, dir, args...); err != nil {
			os.RemoveAll(dir)
			return "", fmt.Errorf("copying the current commit: %w", err)
		}
	}
	os.RemoveAll(filepath.Join(dir, ".git", "hooks"))
	return filepath.Join(dir, ".git"), nil
}

// write packs gitDir (if any) as .git, then files (relative to Root).
func (pk *Packed) write(ctx context.Context, gitDir string, files []string) error {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	add := func(name, full string) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		fi, err := os.Lstat(full)
		if err != nil || fi.IsDir() && name != ".git" && !strings.HasPrefix(name, ".git/") {
			return nil // gone since it was listed, or a submodule
		}
		link := ""
		if fi.Mode()&fs.ModeSymlink != 0 {
			if link, err = os.Readlink(full); err != nil {
				return err
			}
		}
		h, err := tar.FileInfoHeader(fi, link)
		if err != nil {
			return err
		}
		h.Name, h.Uid, h.Gid, h.Uname, h.Gname = name, 0, 0, "", ""
		if fi.IsDir() {
			h.Name += "/"
		}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if fi.Mode().IsRegular() {
			f, err := os.Open(full)
			if err != nil {
				return err
			}
			defer f.Close()
			if _, err := io.Copy(tw, f); err != nil {
				return err
			}
			if buf.Len() > maxPack {
				return fmt.Errorf("%s is larger than %d MB packed: hand off a smaller folder", pk.Root, maxPack>>20)
			}
		}
		return nil
	}
	if gitDir != "" {
		err := filepath.WalkDir(gitDir, func(p string, _ fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(gitDir, p)
			return add(path.Join(".git", filepath.ToSlash(rel)), p)
		})
		if err != nil {
			return err
		}
	}
	for _, f := range files {
		if err := add(f, filepath.Join(pk.Root, filepath.FromSlash(f))); err != nil {
			return err
		}
		pk.Files++
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	pk.Data = buf.Bytes()
	return nil
}

// plainFiles lists a folder that isn't in a git repo, leaving out secret
// files and any .git folder.
func plainFiles(root string, skipped *[]string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		switch {
		case rel == ".":
		case d.IsDir() && d.Name() == ".git":
			return filepath.SkipDir
		case d.IsDir():
		case secret(d.Name()):
			*skipped = append(*skipped, rel)
		default:
			files = append(files, rel)
		}
		return nil
	})
	return files, err
}

// secret reports whether a file's name says it holds credentials: these
// stay on this machine, as the agent's keys reach a VM only through
// forgerun's proxy.
func secret(name string) bool {
	n := strings.ToLower(name)
	switch {
	case n == ".env", n == ".netrc", n == ".npmrc", n == ".pypirc", n == ".git-credentials":
		return true
	case strings.HasPrefix(n, ".env."):
		rest := strings.TrimPrefix(n, ".env.")
		return rest != "example" && rest != "sample" && rest != "template"
	case strings.HasPrefix(n, "id_rsa"), strings.HasPrefix(n, "id_ecdsa"), strings.HasPrefix(n, "id_ed25519"), strings.HasPrefix(n, "id_dsa"):
		return !strings.HasSuffix(n, ".pub")
	}
	for _, ext := range []string{".pem", ".key", ".p12", ".pfx", ".keystore", ".jks"} {
		if strings.HasSuffix(n, ext) {
			return true
		}
	}
	return false
}

// remoteRepo is the GitHub repo a branch's remote (or origin) points at, as
// https://github.com/owner/name, or "" if it isn't on GitHub. SSH host
// aliases (Host gh in ~/.ssh/config) are resolved with ssh -G.
func remoteRepo(ctx context.Context, dir, branch string) string {
	remote := "origin"
	if branch != "" {
		if r, err := git(ctx, dir, "config", "branch."+branch+".remote"); err == nil && r != "" {
			remote = r
		}
	}
	u, err := git(ctx, dir, "remote", "get-url", remote)
	if err != nil {
		return ""
	}
	host, repoPath, ok := splitRemote(u)
	if !ok {
		return ""
	}
	if host != "github.com" {
		out, err := exec.CommandContext(ctx, "ssh", "-G", host).Output()
		if err != nil {
			return ""
		}
		for l := range strings.SplitSeq(string(out), "\n") {
			if h, ok := strings.CutPrefix(l, "hostname "); ok {
				host = strings.ToLower(strings.TrimSpace(h))
			}
		}
	}
	parts := strings.Split(strings.TrimSuffix(strings.Trim(repoPath, "/"), ".git"), "/")
	if host != "github.com" || len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	return "https://github.com/" + parts[0] + "/" + parts[1]
}

// splitRemote reads a remote URL's host and path: https://[user@]host/path,
// ssh://[user@]host[:port]/path, or scp-like [user@]host:path.
func splitRemote(u string) (host, p string, ok bool) {
	if scheme, rest, found := strings.Cut(u, "://"); found {
		if scheme != "https" && scheme != "http" && scheme != "ssh" {
			return "", "", false
		}
		hostPart, p, _ := strings.Cut(rest, "/")
		if _, h, found := strings.Cut(hostPart, "@"); found {
			hostPart = h
		}
		host, _, _ = strings.Cut(hostPart, ":")
		return strings.ToLower(host), p, host != ""
	}
	hostPart, p, found := strings.Cut(u, ":")
	if !found || strings.Contains(hostPart, "/") {
		return "", "", false // a local path
	}
	if _, h, found := strings.Cut(hostPart, "@"); found {
		hostPart = h
	}
	return strings.ToLower(hostPart), p, hostPart != ""
}

// git runs git in dir and returns its trimmed output.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimRight(string(out), "\n"), nil
}
