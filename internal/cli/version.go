// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// version is the release version, set at link time by a release build
// (-ldflags "-X github.com/agaetis-it/takecare-agents/internal/cli.version=v0.5.0").
// Empty, the module version go install recorded is used.
var version string

// BuildInfo is what takecare version prints.
type BuildInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	GoVersion string `json:"goVersion"`
}

// buildInfo reads the version from the link-time value or the build
// information, and the commit from the build information.
func buildInfo(read func() (*debug.BuildInfo, bool)) BuildInfo {
	b := BuildInfo{Version: version, Commit: "unknown", GoVersion: runtime.Version()}
	if info, ok := read(); ok {
		if b.Version == "" {
			b.Version = info.Main.Version
		}
		modified := false
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				b.Commit = s.Value
			case "vcs.modified":
				modified = s.Value == "true"
			}
		}
		if modified {
			b.Commit += "-dirty"
		}
	}
	if b.Version == "" {
		b.Version = "unknown"
	}
	return b
}

func (a *app) version(args []string) int {
	var c common
	fs := a.newFlags("version")
	c.register(fs)
	if code := a.parse(fs, &c, args); code >= 0 {
		return code
	}
	b := buildInfo(debug.ReadBuildInfo)
	if c.output == outputJSON {
		if err := a.printJSON(b); err != nil {
			return a.fail("version: %v", err)
		}
		return ExitOK
	}
	if _, err := fmt.Fprintf(a.stdout, "takecare %s\ncommit %s\nbuilt with %s\n", b.Version, b.Commit, b.GoVersion); err != nil {
		return a.fail("version: %v", err)
	}
	return ExitOK
}
