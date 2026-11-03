package api

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"regexp"
	"strings"

	"github.com/depak7/forgerun/internal/forge"
	"github.com/depak7/forgerun/internal/store"
)

// DefaultMaxFolder is the largest folder upload, compressed, unless the
// server sets another.
const DefaultMaxFolder = 50 << 20

// maxUnpacked bounds a folder's size once unpacked, per compressed byte:
// a small upload mustn't fill a VM's disk.
const maxUnpacked = 20

// folderJSON is an uploaded folder.
type folderJSON struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	SizeBytes int64  `json:"size_bytes"`
	Repo      string `json:"repo"` // its GitHub repo, so its thread can be pushed; "" if none
}

// uploadFolder serves POST /api/folders?name=<name>: the body is a gzipped
// tar of a local folder (or one file), which tasks can then work on instead
// of a GitHub repo ({"folder": id} in place of "repo"). For a folder from a
// GitHub repo, &repo=https://github.com/o/n&base=<commit>&branch=<name>
// let its thread be pushed: onto that commit, with a pull request to that
// branch.
func (s *Server) uploadFolder(w http.ResponseWriter, r *http.Request) {
	if !s.allowTurn(w, r) {
		return
	}
	q := r.URL.Query()
	name := folderName(q.Get("name"))
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required: the folder's name, e.g. ?name=my-app")
		return
	}
	f := store.Folder{Name: name, Owner: viewerOf(r).ID, Branch: q.Get("branch")}
	if repo := q.Get("repo"); repo != "" {
		gh, err := forge.ParseRepo(repo)
		if err != nil || !commitID.MatchString(q.Get("base")) || !validBranch(f.Branch) {
			writeError(w, http.StatusBadRequest, "repo needs base (the commit the folder is on) and a valid branch, or none: "+repo)
			return
		}
		f.Repo, f.Base = "https://"+gh.Host+"/"+gh.Owner+"/"+gh.Name, q.Get("base")
	} else {
		f.Branch = ""
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.maxFolder))
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("the folder is larger than %d MB compressed; leave out what the agent doesn't need", s.maxFolder>>20))
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "reading the folder: "+err.Error())
		return
	}
	if err := checkFolder(data, int64(len(data))*maxUnpacked); err != nil {
		writeError(w, http.StatusBadRequest, "the body must be a gzipped tar of the folder: "+err.Error())
		return
	}
	id := make([]byte, 8)
	rand.Read(id)
	f.ID, f.SizeBytes = "f-"+hex.EncodeToString(id), int64(len(data))
	if err := s.store.SaveFolder(r.Context(), f, data); err != nil {
		s.internalError(w, r, err)
		return
	}
	writeData(w, http.StatusCreated, folderJSON{ID: f.ID, Name: f.Name, SizeBytes: f.SizeBytes, Repo: f.Repo})
}

// commitID is a full git commit ID (SHA-1 or SHA-256).
var commitID = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

// validBranch accepts a branch name ("" = none: the folder was on a
// detached commit) that can safely be passed on to git and GitHub.
func validBranch(b string) bool {
	return b == "" || len(b) <= 200 && !strings.HasPrefix(b, "-") && !strings.Contains(b, "..") && !strings.ContainsAny(b, " ~^:?*[\\\x00")
}

// folderName is what a thread shows for its folder: the last part of the
// name given, without anything that would read as a URL or a path.
func folderName(name string) string {
	name = path.Base(strings.TrimSpace(strings.ReplaceAll(name, "\\", "/")))
	if name == "." || name == ".." || name == "/" || len(name) > 100 || strings.ContainsAny(name, ":\x00") {
		return ""
	}
	return name
}

// checkFolder reads a gzipped tar through, refusing anything else, entries
// that would land outside the folder, and more than max bytes unpacked.
func checkFolder(data []byte, max int64) error {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	tr := tar.NewReader(zr)
	var total int64
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if p := path.Clean(h.Name); path.IsAbs(p) || p == ".." || strings.HasPrefix(p, "../") {
			return fmt.Errorf("%q is outside the folder", h.Name)
		}
		if total += h.Size; total > max {
			return fmt.Errorf("it unpacks to more than %d MB", max>>20)
		}
	}
}

// ownFolder returns the folder if the viewer may start tasks on it,
// writing the error response itself if not.
func (s *Server) ownFolder(w http.ResponseWriter, r *http.Request, id string) (store.Folder, bool) {
	f, err := s.store.GetFolder(r.Context(), id)
	v := viewerOf(r)
	if errors.Is(err, store.ErrFolderNotFound) || (err == nil && !v.Admin && (v.ID == 0 || f.Owner != v.ID)) {
		writeError(w, http.StatusNotFound, "no such folder: "+id+"; upload it first (POST /api/folders)")
		return store.Folder{}, false
	}
	if err != nil {
		s.internalError(w, r, err)
		return store.Folder{}, false
	}
	return f, true
}
