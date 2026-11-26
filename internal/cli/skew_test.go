// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/golden"
)

const (
	chart05 = "0.5.0"
	cli05   = "v0.5.0"
	cli07   = "v0.7.0"
	devVer  = "unknown"
)

func labeledPlatform(name, version string) *v1alpha1.TakeCarePlatform {
	p := &v1alpha1.TakeCarePlatform{ObjectMeta: meta(name, "", 0)}
	if version != "" {
		p.Labels = map[string]string{versionLabel: version}
	}
	return p
}

// Spec 13 §3: one minor version apart is tolerated, kubectl-style; more
// is one line on stderr in text output, a field in JSON output; unknown
// says nothing. The golden holds what each case prints.
func TestVersionSkewWarnings(t *testing.T) {
	var report bytes.Buffer
	for _, tc := range []struct {
		name, cli, cluster string
		warns              bool
	}{
		{"same", cli05, chart05, false},
		{"same minor, other patch", "v0.5.2", chart05, false},
		{"takecare one minor ahead", "v0.6.0", chart05, false},
		{"takecare one minor behind", "v0.4.1", chart05, false},
		{"takecare two minors ahead", cli07, chart05, true},
		{"takecare two minors behind", "v0.3.1", chart05, true},
		{"another major", "v1.5.0", chart05, true},
		{"release candidate", "v0.7.0-rc.1", chart05, true},
		{"go install pseudo-version", "v0.5.1-0.20261126101500-28fd06618bd6", chart05, false},
		{"cluster version unknown", cli07, "", false},
		{"cluster version not a version", cli07, "latest", false},
		{"dev build", devVer, chart05, false},
		{"go build without a tag", "v0.0.0-20261126101500-28fd06618bd6", chart05, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := skew(tc.cli, tc.cluster)
			if (s != nil) != tc.warns {
				t.Fatalf("skew(%q, %q) = %+v, want a warning: %v", tc.cli, tc.cluster, s, tc.warns)
			}
			var text, js bytes.Buffer
			(&app{stderr: &text}).warnSkew(common{output: outputText}, s)
			(&app{stderr: &js}).warnSkew(common{output: outputJSON}, s)
			if js.Len() != 0 {
				t.Errorf("--output json printed on stderr: %q", js.String())
			}
			field, err := json.Marshal(struct {
				VersionSkew *VersionSkew `json:"versionSkew,omitempty"`
			}{s})
			if err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&report, "## %s: takecare %s, cluster %q\nstderr: %q\njson: %s\n\n", tc.name, tc.cli, tc.cluster, text.String(), field)
		})
	}
	golden.Assert(t, "version-skew.txt", report.Bytes())
}

// The platform the chart renders carries the version; a platform without
// the label, such as one a person wrote, leaves it unknown.
func TestPlatformVersion(t *testing.T) {
	for _, tc := range []struct {
		name  string
		items []v1alpha1.TakeCarePlatform
		want  string
	}{
		{"none", nil, ""},
		{"no label", []v1alpha1.TakeCarePlatform{*labeledPlatform("main", "")}, ""},
		{"labeled", []v1alpha1.TakeCarePlatform{*labeledPlatform("main", chart05)}, chart05},
		{"first labeled in name order", []v1alpha1.TakeCarePlatform{
			*labeledPlatform("main", chart05), *labeledPlatform("a", ""), *labeledPlatform("b", "0.4.0"),
		}, "0.4.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := platformVersion(tc.items); got != tc.want {
				t.Errorf("platformVersion = %q, want %q", got, tc.want)
			}
		})
	}
}

// countingLister counts its List calls and refuses them with err.
type countingLister struct {
	calls int
	err   error
}

func (c *countingLister) List(context.Context, client.ObjectList, ...client.ListOption) error {
	c.calls++
	return c.err
}

// The check never fails a command: a platform it cannot read leaves the
// version unknown; a dev build makes no cluster call at all.
func TestClusterSkewNeverFails(t *testing.T) {
	forbidden := &countingLister{err: fmt.Errorf("takecareplatforms is forbidden")}
	if s := (&app{cliVersion: "v0.9.0"}).clusterSkew(t.Context(), forbidden); s != nil || forbidden.calls != 1 {
		t.Errorf("forbidden: skew %+v, %d calls", s, forbidden.calls)
	}
	dev := &countingLister{}
	if s := (&app{cliVersion: "(devel)"}).clusterSkew(t.Context(), dev); s != nil || dev.calls != 0 {
		t.Errorf("dev build: skew %+v, %d calls", s, dev.calls)
	}
	r := fakeReader(t, labeledPlatform("main", chart05))
	if s := (&app{cliVersion: "v0.9.0"}).clusterSkew(t.Context(), r); s == nil || s.Cluster != chart05 {
		t.Errorf("labeled platform: skew %+v", s)
	}
}

// status reads the version from the platforms it lists anyway.
func TestStatusReadsClusterVersion(t *testing.T) {
	s, err := readStatus(t.Context(), fakeReader(t, labeledPlatform("main", chart05)), testNS)
	if err != nil {
		t.Fatal(err)
	}
	if s.clusterVersion != chart05 {
		t.Errorf("clusterVersion = %q", s.clusterVersion)
	}
}

// review warns once the cluster answered, and its JSON carries the field.
func TestReviewWarnsOnSkew(t *testing.T) {
	objs := append(surveyObjects(v1alpha1.RunCompleted, true), labeledPlatform("main", "0.3.0"))
	for _, output := range []string{outputText, outputJSON} {
		t.Run(output, func(t *testing.T) {
			var out, errOut bytes.Buffer
			a := &app{stdin: strings.NewReader(""), stdout: &out, stderr: &errOut, now: func() time.Time { return testNow }, cliVersion: cli05}
			var o reviewOpts
			fs := a.newFlags(cmdReview)
			o.flags(fs)
			dir := t.TempDir()
			writeTree(t, dir, fixtureMap)
			args := []string{flagAccept, acceptAll, flagNoCommit, flagMapDir, dir, flagStateDir, t.TempDir(), flagOutput, output}
			if code := a.parse(fs, &o.common, args); code >= 0 {
				t.Fatalf("parse: exit %d: %s", code, errOut.String())
			}
			if code := a.runReview(t.Context(), reviewReader(t, objs...), &o); code != ExitOK {
				t.Fatalf("review: exit %d: %s", code, errOut.String())
			}
			const line = "takecare: warning: takecare v0.5.0 and TakeCare 0.3.0 in the cluster are more than one minor version apart"
			if output == outputText {
				if !strings.HasPrefix(errOut.String(), line) || strings.Count(errOut.String(), "\n") < 1 {
					t.Errorf("stderr = %q, want it to start with %q", errOut.String(), line)
				}
				return
			}
			if strings.Contains(errOut.String(), "warning") {
				t.Errorf("--output json printed the warning on stderr: %q", errOut.String())
			}
			var res Review
			if err := json.Unmarshal(out.Bytes(), &res); err != nil {
				t.Fatalf("%v: %s (stderr %s)", err, out.String(), errOut.String())
			}
			if res.VersionSkew == nil || res.VersionSkew.Cluster != "0.3.0" || res.VersionSkew.CLI != cli05 {
				t.Errorf("versionSkew = %+v", res.VersionSkew)
			}
		})
	}
}
