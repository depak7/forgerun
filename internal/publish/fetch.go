package publish

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
	"path/filepath"
	"strings"

	"github.com/depak7/forgerun/internal/forge"
	"github.com/depak7/forgerun/internal/task"
)

// maxFetch bounds the tarball of a fetched repo; a bigger one is refused
// rather than copied into a VM.
const maxFetch = 1 << 30

// Fetcher returns a task.Fetcher that fetches repos on this host with tok
// (the user's, or the server's if it's zero).
func (p *Publisher) Fetcher(tok forge.Token) task.Fetcher { return &hostFetcher{p: p, user: tok} }

type hostFetcher struct {
	p    *Publisher
	user forge.Token
}

var _ task.Fetcher = (*hostFetcher)(nil)

// Fetch makes a shallow clone of repo at ref (or its default branch) in a
// temporary directory and returns it as a gzipped tar. The token is given
// to git through its environment only, so nothing in the copy holds it.
func (f *hostFetcher) Fetch(ctx context.Context, repoURL, ref string) ([]byte, string, error) {
	repo, err := forge.ParseRepo(repoURL)
	if err != nil {
		return nil, "", err
	}
	tok, err := f.p.token(ctx, repo, f.user)
	if err != nil {
		return nil, "", err
	}
	dir, err := os.MkdirTemp("", "forgerun-fetch-")
	if err != nil {
		return nil, "", err
	}
	defer os.RemoveAll(dir)
	g := &git{ctx: ctx, dir: dir, env: authEnv(repo, tok), secret: tok.Reveal()}
	remote := "https://" + repo.Host + "/" + repo.Owner + "/" + repo.Name + ".git"
	if f.p.RemoteURL != nil {
		remote = f.p.RemoteURL(repo)
	}

	// The default branch's name, from the remote's HEAD.
	out, err := g.run("ls-remote", "--symref", remote, "HEAD")
	if err != nil {
		return nil, "", fmt.Errorf("reading %s: %w", repo, err)
	}
	branch := ""
	for _, line := range strings.Split(out, "\n") {
		if rest, ok := strings.CutPrefix(line, "ref: refs/heads/"); ok {
			branch, _, _ = strings.Cut(rest, "\t")
		}
	}
	if branch == "" {
		return nil, "", fmt.Errorf("reading %s: no default branch", repo)
	}
	steps := [][]string{
		{"init", "-q"},
		{"remote", "add", "origin", remote},
		{"fetch", "-q", "--depth=1", "origin", cmp.Or(ref, branch)},
		{"checkout", "-q", "--detach", "FETCH_HEAD"},
	}
	for _, args := range steps {
		if _, err := g.run(args...); err != nil {
			return nil, "", fmt.Errorf("fetching %s: %w", repo, err)
		}
	}
	tarball, err := tarDir(dir)
	if err != nil {
		return nil, "", err
	}
	return tarball, branch, nil
}

// tarDir packs a directory (files, dirs, symlinks, modes) as a gzipped tar.
func tarDir(dir string) ([]byte, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == dir {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		link := ""
		if info.Mode()&fs.ModeSymlink != 0 {
			if link, err = os.Readlink(path); err != nil {
				return err
			}
		}
		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		hdr.Name = filepath.ToSlash(rel)
		if d.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			defer file.Close()
			if _, err := io.Copy(tw, file); err != nil {
				return err
			}
		}
		if buf.Len() > maxFetch {
			return fmt.Errorf("the repo is larger than %d MB; forgerun can't copy it into a VM", maxFetch>>20)
		}
		return nil
	})
	if err == nil {
		err = tw.Close()
	}
	if err == nil {
		err = gz.Close()
	}
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
