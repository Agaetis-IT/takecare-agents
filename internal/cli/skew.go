// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

// versionLabel is the label the chart puts on the TakeCarePlatform it
// renders, holding the chart's appVersion: the version the cluster runs.
const versionLabel = "app.kubernetes.io/version"

// VersionSkew is set when this takecare and the TakeCare the cluster runs
// are more than one minor version apart, kubectl-style (spec 13 §3).
type VersionSkew struct {
	CLI     string `json:"cli"`
	Cluster string `json:"cluster"`
	Message string `json:"message"`
}

// platformVersion is the version label of the platforms, in name order:
// the first one set. Empty when no platform carries it.
func platformVersion(items []v1alpha1.TakeCarePlatform) string {
	ps := slices.Clone(items)
	slices.SortFunc(ps, func(a, b v1alpha1.TakeCarePlatform) int { return cmp.Compare(a.Name, b.Name) })
	for _, p := range ps {
		if v := p.Labels[versionLabel]; v != "" {
			return v
		}
	}
	return ""
}

// skew compares this takecare's version with the cluster's. It returns
// nil when either is unknown (a dev build, a platform without the label)
// or when they are at most one minor version apart.
func skew(cli, cluster string) *VersionSkew {
	if !released(cli) {
		return nil
	}
	cMajor, cMinor, _ := majorMinor(cli)
	kMajor, kMinor, ok := majorMinor(cluster)
	if !ok {
		return nil
	}
	if cMajor == kMajor && cMinor-kMinor <= 1 && kMinor-cMinor <= 1 {
		return nil
	}
	return &VersionSkew{CLI: cli, Cluster: cluster, Message: fmt.Sprintf(
		"takecare %s and TakeCare %s in the cluster are more than one minor version apart: use a takecare of the cluster's version",
		cli, cluster)}
}

// released tells whether this takecare's version is one a release or go
// install gave it. A dev build has none ("unknown", "(devel)"), or
// v0.0.0-<date>-<commit>, the pseudo-version of a build with no release
// tag behind it.
func released(cli string) bool {
	_, _, ok := majorMinor(cli)
	return ok && !strings.HasPrefix(cli, "v0.0.0-")
}

// majorMinor reads the major and minor numbers of a version such as
// v0.5.0, 0.5.0, v0.5.0-rc.1 or a Go pseudo-version.
func majorMinor(v string) (major, minor int, ok bool) {
	parts := strings.SplitN(strings.TrimPrefix(v, "v"), ".", 3)
	if len(parts) < 2 {
		return 0, 0, false
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, false
	}
	minor, err = strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, false
	}
	return major, minor, true
}

// clusterSkew reads the platforms for a command that does not list them
// itself. It never fails the command: a platform it cannot read leaves the
// version unknown, and nothing is said.
func (a *app) clusterSkew(ctx context.Context, r lister) *VersionSkew {
	if !released(a.cliVersion) {
		return nil
	}
	var ps v1alpha1.TakeCarePlatformList
	if err := r.List(ctx, &ps); err != nil {
		return nil
	}
	return skew(a.cliVersion, platformVersion(ps.Items))
}

// warnSkew prints the skew as one line on stderr, in text output only:
// with --output json, the result carries it as a field.
func (a *app) warnSkew(c common, s *VersionSkew) {
	if s == nil || c.output == outputJSON {
		return
	}
	_, _ = fmt.Fprintf(a.stderr, "takecare: warning: %s\n", s.Message)
}
