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

	"github.com/google/go-cmp/cmp"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	sigsyaml "sigs.k8s.io/yaml"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

const (
	clusterChart = "takecare-cluster"
	kagentGroup  = "kagent.dev"
)

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

	t.Run("the server carries its own locks", func(t *testing.T) {
		checkHardening(t, objs, ofKind(objs, "ConfigMap")[0].GetName())
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

// The nodes opt-in (spec 12 §4, cluster.readNodes): off by default; when
// on, one ClusterRole with exactly get, list and watch on nodes, bound to
// the chart's ServiceAccount, and the server's nodes_* tools still
// disabled.
func TestClusterChartReadNodes(t *testing.T) {
	name := releaseName + "-nodes"
	for _, o := range render(t, clusterChart, clusterSets...) {
		if o.GetName() == name {
			t.Errorf("readNodes is off by default, yet %s %s renders", o.GetKind(), o.GetName())
		}
	}
	objs := render(t, clusterChart, append(clusterSets, flagSet, "cluster.readNodes=true")...)
	rules := rulesByRole(t, objs)
	if diff := cmp.Diff([]rbacv1.PolicyRule{nodesRule}, rules[kindClusterRole+"/"+name]); diff != "" {
		t.Errorf("the nodes opt-in's rules (-want +got):\n%s", diff)
	}
	var bound bool
	for _, b := range ofKind(objs, kindClusterBinding) {
		ref, _, _ := unstructured.NestedString(b.Object, "roleRef", "name")
		subjects, _, _ := unstructured.NestedSlice(b.Object, "subjects")
		if ref == name {
			bound = jsonOf(subjects) == `[{"kind":"ServiceAccount","name":"`+ofKind(objs, kindSA)[0].GetName()+`","namespace":"`+releaseNamespace+`"}]`
		}
	}
	if !bound {
		t.Errorf("no ClusterRoleBinding binds %s to the chart's ServiceAccount alone", name)
	}
	checkHardening(t, objs, ofKind(objs, "ConfigMap")[0].GetName())
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
	notes := clusterNotes(t, flagSet, "alerting.url=https://alerts.admin.example/v1/alerts")
	for _, want := range []string{"alertTokenSecretRef:", "url: https://alerts.admin.example/v1/alerts", "credentials_file: /etc/alertmanager/secrets/takecare-alerts/token"} {
		if !strings.Contains(notes, want) {
			t.Errorf("the notes lack %q:\n%s", want, notes)
		}
	}
}

// clusterNotes is the install notes of the workload-cluster chart, with its
// REQUIRED values and these arguments.
func clusterNotes(t *testing.T, args ...string) string {
	t.Helper()
	args = append(append([]string{"install", "takecare-cluster", filepath.Join(repoRoot, "charts", clusterChart), "-n", "takecare-cluster", "--dry-run=client"}, args...), clusterSets...)
	out, err := exec.Command(helmBinary(t), args...).CombinedOutput() //nolint:gosec // the test's own binary and arguments
	if err != nil {
		t.Fatalf("helm install --dry-run: %v\n%s", err, out)
	}
	i := bytes.Index(out, []byte("NOTES:"))
	if i < 0 {
		t.Fatalf("no notes in the install's output:\n%s", out)
	}
	return string(out[i:])
}

// notesConnector is the TakeCareConnector the notes print, decoded: the
// lines from its apiVersion to the first blank line, unindented. Not
// strictly: the placeholders the reader fills in are prose, one with a
// comma that YAML reads as a second key.
func notesConnector(t *testing.T, notes string) *v1alpha1.TakeCareConnector {
	t.Helper()
	start := strings.Index(notes, "  apiVersion: takecare.agaetis.fr/")
	if start < 0 {
		t.Fatalf("the notes print no connector:\n%s", notes)
	}
	block, _, _ := strings.Cut(notes[start:], "\n\n")
	var lines []string
	for line := range strings.SplitSeq(block, "\n") {
		lines = append(lines, strings.TrimPrefix(line, "  "))
	}
	c := &v1alpha1.TakeCareConnector{}
	if err := sigsyaml.Unmarshal([]byte(strings.Join(lines, "\n")), c); err != nil {
		t.Fatalf("the notes' connector does not decode: %v\n%s", err, block)
	}
	return c
}

// The notes' connector carries cluster.extraResources, so that the opt-ins
// the chart grants on the workload cluster (the Intendant's kagent.dev
// agents and sandboxagents, spec 10 §8) are declared on the administration
// cluster too; without any, it declares none.
func TestClusterChartNotesExtraResources(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want []v1alpha1.ResourceRef
	}{
		{"none", nil, nil},
		{"the inventory's opt-ins and a core resource", []string{
			flagSet, "cluster.extraResources[0].group=" + kagentGroup, flagSet, "cluster.extraResources[0].resource=agents",
			flagSet, "cluster.extraResources[1].group=" + kagentGroup, flagSet, "cluster.extraResources[1].resource=sandboxagents",
			flagSet, "cluster.extraResources[2].resource=limitranges",
		}, []v1alpha1.ResourceRef{{Group: kagentGroup, Resource: "agents"}, {Group: kagentGroup, Resource: "sandboxagents"}, {Resource: "limitranges"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := notesConnector(t, clusterNotes(t, tc.args...))
			if c.Spec.Kubernetes == nil {
				t.Fatal("the notes' connector has no kubernetes block")
			}
			if got := c.Spec.Kubernetes.Namespaces; !slices.Equal(got, []v1alpha1.Namespace{"apps", "billing"}) {
				t.Errorf("namespaces = %v", got)
			}
			if got := c.Spec.Kubernetes.ExtraResources; !slices.Equal(got, tc.want) {
				t.Errorf("extraResources = %v, want %v", got, tc.want)
			}
		})
	}
}
