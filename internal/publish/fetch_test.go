package publish

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/depak7/forgerun/internal/forge"
)

// Fetch hands back a shallow checkout (with .git) at the default branch or
// at a given commit, and nothing in it holds the token.
func TestFetch(t *testing.T) {
	f := newFixture(t)
	fetcher := f.publisher(secret).Fetcher(forge.NewToken(secret))

	tarball, branch, err := fetcher.Fetch(context.Background(), "https://github.com/o/r", "")
	if err != nil {
		t.Fatal(err)
	}
	files := untar(t, tarball)
	if branch != "main" || files["later.txt"] == "" || files[".git/HEAD"] == "" || files[".git/shallow"] == "" {
		t.Errorf("default branch: %q, files %v", branch, keys(files))
	}
	for name, body := range files {
		if strings.Contains(body, secret) || strings.Contains(body, "x-access-token") {
			t.Errorf("%s holds the token", name)
		}
	}

	tarball, _, err = fetcher.Fetch(context.Background(), "https://github.com/o/r", f.base)
	if err != nil {
		t.Fatal(err)
	}
	if files := untar(t, tarball); files["later.txt"] != "" || files["README.md"] != "hello\n" {
		t.Errorf("at the base commit: %v", keys(files))
	}

	// A token the remote refuses fails, without the token in the error.
	bad := newFixture(t)
	bad.remote = "/nonexistent/" + secret
	if _, _, err := bad.publisher(secret).Fetcher(forge.NewToken(secret)).Fetch(context.Background(), "https://github.com/o/r", ""); err == nil || strings.Contains(err.Error(), secret) {
		t.Errorf("unreachable remote: %v", err)
	}
}

func untar(t *testing.T, tarball []byte) map[string]string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(tarball))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	files := map[string]string{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return files
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		files[hdr.Name] = string(b)
	}
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		if !strings.HasPrefix(k, ".git/") {
			out = append(out, k)
		}
	}
	return out
}
