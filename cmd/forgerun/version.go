package main

import (
	"regexp"
	"runtime/debug"
)

// version is this build's forgerun version: the module version of a
// released binary ("go install …@vX.Y.Z"), or the commit a source build
// was made from. The go command stamps a source build's commit on the
// module version as a pseudo-version (v0.0.0-<timestamp>-<rev>); precise,
// but unreadable in a settings list, so the commit alone is reported.
// "" when neither is known (a build without VCS stamping); a worker then
// simply reports no version.
func version() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	return buildVersion(bi)
}

// pseudoVersion matches what the go command stamps as the main module's
// version for a build from a source checkout: a timestamp and revision,
// not a release someone chose ("v0.0.0-20261109052411-9da6e0817a22", or
// "v1.2.4-0.20261109052411-9da6e0817a22" after a tag).
var pseudoVersion = regexp.MustCompile(`^v\d+\.\d+\.\d+(-.*)?\d{14}-[0-9a-f]{12}(\+dirty)?$`)

// buildVersion reads bi the way version does; a parameter so tests can
// make builds up.
func buildVersion(bi *debug.BuildInfo) string {
	if v := bi.Main.Version; v != "" && v != "(devel)" && !pseudoVersion.MatchString(v) {
		return v
	}
	var rev, modified string
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			modified = s.Value
		}
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if rev != "" && modified == "true" {
		rev += "-dirty"
	}
	return rev
}
