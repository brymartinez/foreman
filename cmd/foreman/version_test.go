package main

import (
	"runtime/debug"
	"testing"
)

func TestBuildVersion(t *testing.T) {
	info := func(revision, dirty string) *debug.BuildInfo {
		return &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: revision}, {Key: "vcs.modified", Value: dirty}}}
	}
	for _, tc := range []struct {
		name, version string
		info          *debug.BuildInfo
		want          string
	}{
		{"local", "0.1.0-dev", info("0123456789abcdef", "false"), "0.1.0-dev (0123456789ab)"},
		{"dirty", "0.1.0-dev", info("abcdef0123456789", "true"), "0.1.0-dev (abcdef012345-dirty)"},
		{"release", "v1.2.3", info("abcdef", "true"), "v1.2.3"},
		{"no build info", "0.1.0-dev", nil, "0.1.0-dev (unknown)"},
		{"no vcs", "0.1.0-dev", &debug.BuildInfo{}, "0.1.0-dev (unknown)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildVersion(tc.version, tc.info); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
