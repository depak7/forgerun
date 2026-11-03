package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/depak7/forgerun/internal/store"
)

// tokenPrefix starts every personal token, so it's recognisable (and
// secret scanners can find a leaked one).
const tokenPrefix = "frt_"

// tokenJSON is a personal token. Token, its value, is only ever sent once:
// when it's created.
type tokenJSON struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"` // null if never used
	Token      string     `json:"token,omitempty"`
}

func tokenOf(t store.APIToken) tokenJSON {
	return tokenJSON{ID: t.ID, Name: t.Name, CreatedAt: t.CreatedAt.UTC(), LastUsedAt: utc(t.LastUsedAt)}
}

// tokenViewer is who a personal token acts for.
func (s *Server) tokenViewer(r *http.Request, tok string) (viewer, bool) {
	if !strings.HasPrefix(tok, tokenPrefix) {
		return viewer{}, false
	}
	u, err := s.store.TokenUser(r.Context(), loginID(tok))
	if err != nil {
		if !errors.Is(err, store.ErrUserNotFound) {
			s.log.Error("reading a token", "err", err)
		}
		return viewer{}, false
	}
	return viewer{ID: u.ID, Login: u.Login, Admin: s.isAdmin(u.Login), ByToken: true}, true
}

// browserOnly refuses token management unless the viewer signed in with
// GitHub in a browser: a leaked token mustn't be able to make more, or
// outlive its own revocation. In local mode there's nothing to sign in to.
func (s *Server) browserOnly(w http.ResponseWriter, r *http.Request) bool {
	switch {
	case s.auth == nil:
		writeError(w, http.StatusBadRequest, "this server has no sign-in (local mode): tools need no token")
		return false
	case viewerOf(r).ByToken:
		writeError(w, http.StatusForbidden, "tokens are managed from forgerun's Settings page, not with a token")
		return false
	}
	return true
}

// createToken serves POST /api/tokens {"name": "..."}.
func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	if !s.browserOnly(w, r) {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > 100 {
		writeError(w, http.StatusBadRequest, "name the token (up to 100 characters), e.g. where it's used")
		return
	}
	value := tokenPrefix + randomToken()
	t, err := s.store.CreateToken(r.Context(), store.APIToken{UserID: viewerOf(r).ID, Name: name}, loginID(value))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := tokenOf(t)
	out.Token = value
	writeData(w, http.StatusCreated, out)
}

// listTokens serves GET /api/tokens: the viewer's own tokens.
func (s *Server) listTokens(w http.ResponseWriter, r *http.Request) {
	if !s.browserOnly(w, r) {
		return
	}
	ts, err := s.store.ListTokens(r.Context(), viewerOf(r).ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]tokenJSON, 0, len(ts))
	for _, t := range ts {
		out = append(out, tokenOf(t))
	}
	writeData(w, http.StatusOK, out)
}

// deleteToken serves DELETE /api/tokens/{id}: revokes one of the viewer's
// tokens, at once.
func (s *Server) deleteToken(w http.ResponseWriter, r *http.Request) {
	if !s.browserOnly(w, r) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err == nil {
		err = s.store.DeleteToken(r.Context(), viewerOf(r).ID, id)
	}
	var badID *strconv.NumError
	switch {
	case errors.Is(err, store.ErrTokenNotFound) || errors.As(err, &badID):
		writeError(w, http.StatusNotFound, "no such token")
	case err != nil:
		s.internalError(w, r, err)
	default:
		writeData(w, http.StatusOK, struct{}{})
	}
}
