// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package releasesserver

import (
	"cmp"
	"slices"
	"strings"

	"golang.org/x/mod/semver"
)

// maxVersions bounds versions_list's answer (spec 12 §7.1).
const maxVersions = 200

// Version is one published version.
type Version struct {
	Version    string `json:"version"`
	Prerelease bool   `json:"prerelease,omitempty"`
	Published  string `json:"published,omitempty"`
}

// order keeps the versions that parse as semantic versions (a leading v
// optional, as Go's semver wants it), flags pre-releases, and sorts them
// newest first; it returns at most maxVersions and how many it left out.
func order(in []published) ([]Version, int) {
	type parsed struct {
		Version
		canonical string
	}
	seen := map[string]bool{}
	all := make([]parsed, 0, len(in))
	for _, p := range in {
		v := p.tag
		if !strings.HasPrefix(v, "v") {
			v = "v" + v
		}
		if !semver.IsValid(v) || seen[p.tag] || len(p.tag) > 128 {
			continue
		}
		seen[p.tag] = true
		all = append(all, parsed{Version{Version: p.tag, Prerelease: p.prerelease || semver.Prerelease(v) != "", Published: p.date}, v})
	}
	slices.SortFunc(all, func(a, b parsed) int {
		if c := semver.Compare(b.canonical, a.canonical); c != 0 {
			return c
		}
		return cmp.Compare(a.Version.Version, b.Version.Version)
	})
	omitted := 0
	if len(all) > maxVersions {
		omitted, all = len(all)-maxVersions, all[:maxVersions]
	}
	out := make([]Version, len(all))
	for i, p := range all {
		out[i] = p.Version
	}
	return out, omitted
}
