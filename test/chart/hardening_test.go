// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package chart

import (
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// hardeningLines are the kubernetes-mcp-server's own locks both charts
// render (spec 05): its own account whatever a caller sends, the four tools
// our read-only RBAC can never serve not offered, Secrets refused by the
// server itself; read-only stays.
var hardeningLines = []string{
	"read_only = true",
	`cluster_auth_mode = "kubeconfig"`,
	`disabled_tools = ["nodes_log", "nodes_stats_summary", "nodes_top", "projects_list"]`,
	"[[denied_resources]]",
	`group = ""`,
	`version = "v1"`,
	`kind = "Secret"`,
}

// checkHardening fails unless the server's config.toml, in a ConfigMap of
// that name, carries every hardening line, the disabled list exactly.
func checkHardening(t *testing.T, objs []*unstructured.Unstructured, name string) {
	t.Helper()
	var conf string
	for _, cm := range ofKind(objs, "ConfigMap") {
		if cm.GetName() == name {
			conf, _, _ = unstructured.NestedString(cm.Object, "data", "config.toml")
		}
	}
	lines := strings.Split(conf, "\n")
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}
	for _, want := range hardeningLines {
		if !slices.Contains(lines, want) {
			t.Errorf("%s config.toml lacks %q:\n%s", name, want, conf)
		}
	}
	if n := strings.Count(conf, "disabled_tools"); n != 1 {
		t.Errorf("%s config.toml sets disabled_tools %d times", name, n)
	}
}

// The main chart's kubernetes add-on carries the server's own locks.
func TestKubernetesAddonHardening(t *testing.T) {
	checkHardening(t, render(t, releaseName, profileArgs("demo")...), "takecare-addon-kubernetes")
}
