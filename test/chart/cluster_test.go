// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package chart

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const clusterChart = "takecare-cluster"

// clusterSets fills the workload-cluster chart's REQUIRED values.
var clusterSets = []string{
	flagSet, "cluster.name=paris-1", flagSet, "cluster.namespaces={apps,billing}",
	flagSet, "tls.secretName=front-tls", flagSet, "tls.clientCASecretName=front-client-ca",
	flagSet, "tls.clientNames={admin.example.internal}",
}

// ADR 0016 §1 and §4: the package is read-only, exposes the front's mTLS
// port only, pins its images, and runs the server and the front in one pod.
func TestClusterChart(t *testing.T) {
	objs := render(t, clusterChart, clusterSets...)

	t.Run("every RBAC rule passes the read-only guard", func(t *testing.T) {
		rules := rulesByRole(t, objs)
		if n := len(ofKind(objs, kindRole)); n != 2 || len(rules) != 2 {
			t.Fatalf("%d Roles and %d kinds of rules, want a Role per namespace and the namespaces ClusterRole", n, len(rules))
		}
		for role, rs := range rules {
			for _, r := range rs {
				if why := forbidden(r); why != "" {
					t.Errorf("%s %s", role, why)
				}
				for _, v := range r.Verbs {
					if !slices.Contains([]string{verbGet, verbList, verbWatch}, v) {
						t.Errorf("%s grants %s", role, v)
					}
				}
			}
		}
	})

	t.Run("the Service exposes the front's mTLS port only", func(t *testing.T) {
		svcs := ofKind(objs, "Service")
		ports, _, _ := unstructured.NestedSlice(svcs[0].Object, "spec", "ports")
		if len(svcs) != 1 || len(ports) != 1 || ports[0].(map[string]any)["targetPort"] != "mtls" {
			t.Fatalf("services %d, ports %v", len(svcs), ports)
		}
	})

	t.Run("one pod, the server and the front, both images pinned", func(t *testing.T) {
		deps := ofKind(objs, "Deployment")
		if len(deps) != 1 {
			t.Fatalf("%d Deployments", len(deps))
		}
		containers, _, _ := unstructured.NestedSlice(deps[0].Object, "spec", "template", "spec", "containers")
		names := make([]string, 0, len(containers))
		for _, c := range containers {
			names = append(names, c.(map[string]any)["name"].(string))
		}
		if !slices.Equal(names, []string{"server", "front"}) {
			t.Errorf("containers %v", names)
		}
		for _, img := range images(deps[0]) {
			if !strings.Contains(img, "@sha256:") && !strings.HasPrefix(img, "ghcr.io/agaetis-it/takecare-agents-mcp-front:") {
				t.Errorf("unpinned image %s", img)
			}
		}
		front := containers[1].(map[string]any)
		args := strings.Join(toStrings(front["args"]), " ")
		for _, want := range []string{"--client-names=admin.example.internal", "--guard-namespaces=apps,billing", "--upstream=http://127.0.0.1:8080"} {
			if !strings.Contains(args, want) {
				t.Errorf("front args %q lack %s", args, want)
			}
		}
	})

	t.Run("the server binds the loopback", func(t *testing.T) {
		cm := ofKind(objs, "ConfigMap")[0]
		conf, _, _ := unstructured.NestedString(cm.Object, "data", "config.toml")
		if !strings.Contains(conf, `bind_address = "127.0.0.1"`) || !strings.Contains(conf, "read_only = true") {
			t.Errorf("config.toml:\n%s", conf)
		}
	})

	t.Run("the NetworkPolicy admits the front's mTLS port only", func(t *testing.T) {
		ingress, _, _ := unstructured.NestedSlice(ofKind(objs, "NetworkPolicy")[0].Object, "spec", "ingress")
		if len(ingress) != 1 {
			t.Fatalf("ingress rules %v", ingress)
		}
		ports := ingress[0].(map[string]any)["ports"].([]any)
		if len(ports) != 1 || ports[0].(map[string]any)["port"] != int64(8443) {
			t.Errorf("ingress ports %v", ports)
		}
	})
}

func toStrings(v any) []string {
	items := v.([]any)
	out := make([]string, 0, len(items))
	for _, x := range items {
		out = append(out, x.(string))
	}
	return out
}

// The chart refuses to render without its REQUIRED values, and with an
// extra resource the read-only guard forbids.
func TestClusterChartRefuses(t *testing.T) {
	without := func(key string) []string {
		var out []string
		for i := 1; i < len(clusterSets); i += 2 {
			if !strings.HasPrefix(clusterSets[i], key+"=") {
				out = append(out, flagSet, clusterSets[i])
			}
		}
		return out
	}
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"no cluster name", without("cluster.name"), "cluster.name is REQUIRED"},
		{"a cluster name not a DNS label", append(without("cluster.name"), flagSet, "cluster.name=Paris_1"), "cluster.name is REQUIRED"},
		{"no namespace", without("cluster.namespaces"), "cluster.namespaces is REQUIRED"},
		{"no server certificate", without("tls.secretName"), "tls.secretName is REQUIRED"},
		{"no client CA", without("tls.clientCASecretName"), "tls.clientCASecretName is REQUIRED"},
		{"no client name", without("tls.clientNames"), "tls.clientNames is REQUIRED"},
		{"Secrets as an extra resource", append(slices.Clone(clusterSets), flagSet, "cluster.extraResources[0].resource=secrets"), "refused by the read-only guard"},
		{"pods/exec as an extra resource", append(slices.Clone(clusterSets), flagSet, "cluster.extraResources[0].resource=pods/exec"), "refused by the read-only guard"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := helmTemplate(t, clusterChart, "takecare-cluster", tc.args...)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if err := cmd.Run(); err == nil || !strings.Contains(stderr.String(), tc.want) {
				t.Errorf("err %v, stderr %q, want %q", err, stderr.String(), tc.want)
			}
		})
	}
}

// The install notes print the connector, its alert token reference, and
// the receiver of this cluster's Alertmanager with the intake's URL and a
// token file, never a value (ADR 0016 §5).
func TestClusterChartNotes(t *testing.T) {
	args := append([]string{"install", "takecare-cluster", filepath.Join(repoRoot, "charts", clusterChart), "-n", "takecare-cluster", "--dry-run=client",
		flagSet, "alerting.url=https://alerts.admin.example/v1/alerts"}, clusterSets...)
	out, err := exec.Command(helmBinary(t), args...).CombinedOutput() //nolint:gosec // the test's own binary and arguments
	if err != nil {
		t.Fatalf("helm install --dry-run: %v\n%s", err, out)
	}
	notes := string(out[bytes.Index(out, []byte("NOTES:")):])
	for _, want := range []string{"alertTokenSecretRef:", "url: https://alerts.admin.example/v1/alerts", "credentials_file: /etc/alertmanager/secrets/takecare-alerts/token"} {
		if !strings.Contains(notes, want) {
			t.Errorf("the notes lack %q:\n%s", want, notes)
		}
	}
}
