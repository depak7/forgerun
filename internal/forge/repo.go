package forge

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// ErrUnsupportedHost means forgerun can't open pull requests on the repo's
// host (yet). Tasks on such repos still work; they just can't publish.
var ErrUnsupportedHost = errors.New("pull requests aren't supported for this host")

// Repo identifies a repository on a forge, e.g. github.com/depak7/playground.
type Repo struct {
	Host  string // lower-case, e.g. "github.com"
	Owner string
	Name  string
}

func (r Repo) String() string { return r.Host + "/" + r.Owner + "/" + r.Name }

// supportedHosts are the forges forgerun can publish to.
var supportedHosts = map[string]bool{"github.com": true}

// ParseRepo reads an https clone URL like https://github.com/owner/name(.git).
func ParseRepo(raw string) (Repo, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return Repo{}, fmt.Errorf("repo %q: want an https URL like https://github.com/owner/name", raw)
	}
	host := strings.ToLower(u.Host)
	if !supportedHosts[host] {
		return Repo{}, fmt.Errorf("%w: %s", ErrUnsupportedHost, host)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return Repo{}, fmt.Errorf("repo %q: want https://%s/owner/name", raw, host)
	}
	name := strings.TrimSuffix(parts[1], ".git")
	if name == "" {
		return Repo{}, fmt.Errorf("repo %q: missing repository name", raw)
	}
	return Repo{Host: host, Owner: parts[0], Name: name}, nil
}
