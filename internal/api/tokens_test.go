package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// A personal token acts as its user from a tool: their tasks and folders,
// nobody else's. Tokens are made and revoked only from a browser login.
func TestPersonalTokens(t *testing.T) {
	h, _, _ := authSetup(t)
	alice := signIn(t, h, "alice")

	var tok tokenJSON
	json.Unmarshal(mustData(t, as(h, alice, "POST", "/api/tokens", `{"name":"laptop"}`), http.StatusCreated), &tok)
	if !strings.HasPrefix(tok.Token, tokenPrefix) || tok.Name != "laptop" || tok.ID == 0 {
		t.Fatalf("created %+v", tok)
	}
	bearer := []string{"Authorization", "Bearer " + tok.Token}

	// From a tool: no cookie, no Origin, and it's alice.
	var me meJSON
	json.Unmarshal(mustData(t, do(h, "GET", "/api/me", "", bearer...), http.StatusOK), &me)
	if !me.SignedIn || me.Login != "alice" {
		t.Errorf("me = %+v", me)
	}
	f := struct{ ID string }{}
	json.Unmarshal(mustData(t, do(h, "POST", "/api/folders?name=app", folderTar(t, map[string]string{"a": "a"}), bearer...), http.StatusCreated), &f)
	tk := decodeTask(t, mustData(t, do(h, "POST", "/api/tasks", `{"folder":"`+f.ID+`","prompt":"p"}`, bearer...), http.StatusCreated))
	if tk.Folder != f.ID {
		t.Errorf("task %+v", tk)
	}
	// Bob can't use alice's folder, signed in or not.
	bob := signIn(t, h, "bob")
	decode(t, as(h, bob, "POST", "/api/tasks", `{"folder":"`+f.ID+`","prompt":"p"}`), http.StatusNotFound)

	// The listing never shows the value again, but shows it was used.
	var list []tokenJSON
	json.Unmarshal(mustData(t, as(h, alice, "GET", "/api/tokens", ""), http.StatusOK), &list)
	if len(list) != 1 || list[0].Token != "" || list[0].LastUsedAt == nil {
		t.Errorf("list = %+v", list)
	}
	// A token can't manage tokens, so a leaked one can't make more.
	decode(t, do(h, "POST", "/api/tokens", `{"name":"more"}`, bearer...), http.StatusForbidden)
	decode(t, do(h, "DELETE", "/api/tokens/"+strconv.FormatInt(tok.ID, 10), "", bearer...), http.StatusForbidden)
	// Only its owner can revoke it; after that it's refused.
	decode(t, as(h, bob, "DELETE", "/api/tokens/"+strconv.FormatInt(tok.ID, 10), ""), http.StatusNotFound)
	mustData(t, as(h, alice, "DELETE", "/api/tokens/"+strconv.FormatInt(tok.ID, 10), ""), http.StatusOK)
	decode(t, do(h, "GET", "/api/me", "", bearer...), http.StatusUnauthorized)

	decode(t, do(h, "GET", "/api/tasks", "", "Authorization", "Bearer frt_made-up"), http.StatusUnauthorized)
	decode(t, as(h, alice, "POST", "/api/tokens", `{"name":" "}`), http.StatusBadRequest)
	decode(t, as(h, alice, "DELETE", "/api/tokens/abc", ""), http.StatusNotFound)
}

func TestNoTokensInLocalMode(t *testing.T) {
	h, _, _ := setup(t)
	_, apiErr := decode(t, do(h, "POST", "/api/tokens", `{"name":"x"}`), http.StatusBadRequest)
	if !strings.Contains(apiErr.Message, "local mode") {
		t.Errorf("message %q", apiErr.Message)
	}
}
