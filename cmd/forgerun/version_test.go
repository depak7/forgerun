package main

import (
	"runtime/debug"
	"testing"
)

// A released build reports its module version; a source build its commit.
func TestBuildVersion(t *testing.T) {
	commit := "89a3d38bfd7e379557cd09982d5a81de80e97d5a"
	vcs := []debug.BuildSetting{{Key: "vcs.revision", Value: commit}}
	for _, tc := range []struct {
		name string
		bi   *debug.BuildInfo
		want string
	}{
		{"module version", &debug.BuildInfo{Main: debug.Module{Version: "v0.4.2"}}, "v0.4.2"},
		{"prerelease module version", &debug.BuildInfo{Main: debug.Module{Version: "v1.0.0-rc1"}}, "v1.0.0-rc1"},
		// What the go command stamps on a source build since Go 1.24.
		{"source build, untagged", &debug.BuildInfo{Main: debug.Module{Version: "v0.0.0-20261109052411-89a3d38bfd7e"},
			Settings: vcs}, commit[:12]},
		{"source build, after a tag", &debug.BuildInfo{Main: debug.Module{Version: "v0.4.3-0.20261109052411-89a3d38bfd7e"},
			Settings: vcs}, commit[:12]},
		{"source build, uncommitted changes", &debug.BuildInfo{Main: debug.Module{Version: "v0.0.0-20261109052411-89a3d38bfd7e+dirty"},
			Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: commit}, {Key: "vcs.modified", Value: "true"}}}, commit[:12] + "-dirty"},
		{"devel with vcs", &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}, Settings: vcs}, commit[:12]},
		{"devel without vcs", &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, ""},
		{"no version at all", &debug.BuildInfo{}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildVersion(tc.bi); got != tc.want {
				t.Errorf("buildVersion = %q, want %q", got, tc.want)
			}
		})
	}
}
