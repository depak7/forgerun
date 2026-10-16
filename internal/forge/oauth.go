package forge

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// OAuth signs people in with a GitHub OAuth App: they approve it on
// github.com, and forgerun exchanges the code it gets back for their token.
type OAuth struct {
	ClientID     string
	ClientSecret string
	BaseURL      string       // "" = https://github.com; tests point it at a fake
	HTTP         *http.Client // nil = a client with a 30s timeout
}

// Scopes forgerun asks for: their repos (to clone private ones, push and
// open pull requests as them) and their profile.
const Scopes = "repo read:user"

// AuthorizeURL is where to send someone to approve forgerun. state comes
// back unchanged to the callback, which checks it.
func (o *OAuth) AuthorizeURL(redirectURI, state string) string {
	q := url.Values{"client_id": {o.ClientID}, "redirect_uri": {redirectURI}, "scope": {Scopes}, "state": {state}, "allow_signup": {"true"}}
	return o.base() + "/login/oauth/authorize?" + q.Encode()
}

// Exchange turns the code GitHub sent to the callback into the user's token.
func (o *OAuth) Exchange(ctx context.Context, code, redirectURI string) (Token, error) {
	form := url.Values{"client_id": {o.ClientID}, "client_secret": {o.ClientSecret}, "code": {code}, "redirect_uri": {redirectURI}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.base()+"/login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return Token{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := (&GitHub{HTTP: o.HTTP}).client().Do(req)
	if err != nil {
		return Token{}, fmt.Errorf("signing in with GitHub: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return Token{}, fmt.Errorf("signing in with GitHub: HTTP %d: unexpected response", resp.StatusCode)
	}
	if out.Error != "" || out.AccessToken == "" {
		return Token{}, fmt.Errorf("signing in with GitHub: %s", cmp.Or(out.Description, out.Error, "no token in the response"))
	}
	return NewToken(out.AccessToken), nil
}

func (o *OAuth) base() string { return strings.TrimRight(cmp.Or(o.BaseURL, "https://github.com"), "/") }
