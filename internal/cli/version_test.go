// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"bytes"
	"encoding/json"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

const devel = "(devel)"

func TestBuildInfo(t *testing.T) {
	settings := func(kv ...string) []debug.BuildSetting {
		s := make([]debug.BuildSetting, 0, len(kv)/2)
		for pair := range slices.Chunk(kv, 2) {
			s = append(s, debug.BuildSetting{Key: pair[0], Value: pair[1]})
		}
		return s
	}
	for _, tc := range []struct {
		name   string
		linked string
		info   *debug.BuildInfo
		want   BuildInfo
	}{
		{"go install @version", "", &debug.BuildInfo{Main: debug.Module{Version: "v0.5.0"}, Settings: settings("vcs.revision", "abc123")},
			BuildInfo{Version: "v0.5.0", Commit: "abc123"}},
		{"release build: the linked version wins", "v0.5.1", &debug.BuildInfo{Main: debug.Module{Version: devel}, Settings: settings("vcs.revision", "abc123", "vcs.modified", "false")},
			BuildInfo{Version: "v0.5.1", Commit: "abc123"}},
		{"modified tree", "", &debug.BuildInfo{Main: debug.Module{Version: devel}, Settings: settings("vcs.revision", "abc123", "vcs.modified", "true")},
			BuildInfo{Version: devel, Commit: "abc123-dirty"}},
		{"no build information", "", nil, BuildInfo{Version: "unknown", Commit: "unknown"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := version
			version = tc.linked
			t.Cleanup(func() { version = old })
			got := buildInfo(func() (*debug.BuildInfo, bool) { return tc.info, tc.info != nil })
			tc.want.GoVersion = runtime.Version()
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("(-want +got):\n%s", diff)
			}
		})
	}
}

// version needs no cluster: it runs with no kubeconfig at all.
func TestVersionCommand(t *testing.T) {
	t.Setenv("KUBECONFIG", "/nonexistent/kubeconfig")
	var out, errOut bytes.Buffer
	a := &app{stdout: &out, stderr: &errOut}
	if got := a.run(t.Context(), []string{cmdVersion, flagOutput, outputJSON}); got != ExitOK {
		t.Fatalf("exit %d, stderr %q", got, errOut.String())
	}
	var b BuildInfo
	if err := json.Unmarshal(out.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	if b.Version == "" || b.Commit == "" || b.GoVersion != runtime.Version() {
		t.Errorf("version --output json = %+v", b)
	}
	out.Reset()
	if got := a.run(t.Context(), []string{cmdVersion, "--no-color", "--accessible"}); got != ExitOK {
		t.Fatalf("exit %d, stderr %q", got, errOut.String())
	}
	if !strings.HasPrefix(out.String(), "takecare "+b.Version+"\n") {
		t.Errorf("version = %q", out.String())
	}
}
