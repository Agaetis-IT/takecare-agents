// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary

package chart

import (
	"path"
	"reflect"
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	sigsyaml "sigs.k8s.io/yaml"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

func byName(objs []*unstructured.Unstructured, kind, name string) *unstructured.Unstructured {
	for _, o := range ofKind(objs, kind) {
		if o.GetName() == name {
			return o
		}
	}
	return nil
}

// The demo-corpus add-on (spec 05): one server and one connector per source,
// the corpus mounted from the ConfigMap the TakeCareDemo controller writes,
// reached by the gateway and the operator only, with no egress.
func TestDemoCorpusAddon(t *testing.T) {
	demo := render(t, releaseName, profileArgs("demo")...)
	for _, src := range []struct {
		name string
		typ  v1alpha1.ConnectorType
	}{
		{"incidents", v1alpha1.ConnectorKnowledge}, {"runbooks", v1alpha1.ConnectorKnowledge},
		{"deployments", v1alpha1.ConnectorChanges}, {"vendor", v1alpha1.ConnectorKnowledge},
		{"support", v1alpha1.ConnectorTickets},
	} {
		t.Run(src.name, func(t *testing.T) {
			addon := "corpus-" + src.name
			conn := byName(demo, "TakeCareConnector", src.name)
			if conn == nil {
				t.Fatal("no connector")
			}
			types, _, _ := unstructured.NestedStringSlice(conn.Object, "spec", "types")
			got, _, _ := unstructured.NestedString(conn.Object, "spec", "mcp", "addon")
			if len(types) != 1 || types[0] != string(src.typ) || got != addon {
				t.Errorf("connector types %v addon %q, want [%s] %q", types, got, src.typ, addon)
			}
			dep := byName(demo, "Deployment", "takecare-addon-"+addon)
			if dep == nil {
				t.Fatal("no Deployment")
			}
			volumes, _, _ := unstructured.NestedSlice(dep.Object, "spec", "template", "spec", "volumes")
			cm, _, _ := unstructured.NestedMap(volumes[0].(map[string]any), "configMap")
			if cm["name"] != "takecare-demo-corpus-"+src.name || cm["optional"] != true {
				t.Errorf("corpus volume %v, want the optional ConfigMap takecare-demo-corpus-%s", cm, src.name)
			}
			containers, _, _ := unstructured.NestedSlice(dep.Object, "spec", "template", "spec", "containers")
			args, _, _ := unstructured.NestedStringSlice(containers[0].(map[string]any), "args")
			if !strings.Contains(strings.Join(args, " "), "--prefix="+src.name+" ") {
				t.Errorf("args %v do not prefix the tools with %s", args, src.name)
			}
			if byName(demo, "Service", "takecare-addon-"+addon) == nil {
				t.Error("no Service: the operator reaches an add-on at takecare-addon-<name>")
			}
			if byName(demo, "NetworkPolicy", "takecare-addon-"+addon+"-ingress") == nil {
				t.Error("no ingress NetworkPolicy")
			}
			egress := byName(demo, "NetworkPolicy", "takecare-addon-"+addon+"-egress")
			if egress == nil {
				t.Fatal("no egress NetworkPolicy")
			}
			if rules, found, _ := unstructured.NestedSlice(egress.Object, "spec", "egress"); !found || len(rules) != 0 {
				t.Errorf("egress %v, want none at all", rules)
			}
		})
	}
	for _, o := range render(t, releaseName, append(profileArgs("prod"), prodSets...)...) {
		if strings.Contains(o.GetName(), "corpus") {
			t.Errorf("prod renders %s %s", o.GetKind(), o.GetName())
		}
	}
}

func TestDemoCorpusIsDemoOnly(t *testing.T) {
	args := append(append(profileArgs("prod"), prodSets...), flagSet, "addons.demoCorpus.enabled=true")
	out, err := helmTemplate(t, releaseName, releaseNamespace, args...).CombinedOutput()
	if err == nil || !strings.Contains(string(out), "addons.demoCorpus is DEMO ONLY") {
		t.Errorf("err = %v, output:\n%s", err, out)
	}
}

// The demo Alertmanager delivers the scenery's alerts to the Doc's webhook
// with the token the operator checks, mounted where the receiver reads it.
// The demo cluster serves no Prometheus Operator API, so no
// AlertmanagerConfig doubles the delivery.
func TestDemoAlertmanagerDeliversToTheDoc(t *testing.T) {
	demo := render(t, releaseName, profileArgs("demo")...)
	if amc := ofKind(demo, amConfigKind); len(amc) != 0 {
		t.Errorf("the demo renders an AlertmanagerConfig: %s", amc[0].GetName())
	}
	cm := byName(demo, "ConfigMap", "alertmanager")
	if cm == nil {
		t.Fatal("no alertmanager ConfigMap")
	}
	raw, _, _ := unstructured.NestedString(cm.Object, "data", "alertmanager.yml")
	var config struct {
		Route struct {
			Routes []struct {
				Receiver string
				Matchers []string
			}
		}
		Receivers []struct {
			Name           string
			WebhookConfigs []struct {
				URL          string `json:"url"`
				SendResolved bool   `json:"send_resolved"`
				HTTPConfig   struct {
					Authorization struct {
						Type            string
						CredentialsFile string `json:"credentials_file"`
					}
				} `json:"http_config"`
			} `json:"webhook_configs"`
		}
	}
	if err := sigsyaml.Unmarshal([]byte(raw), &config); err != nil {
		t.Fatal(err)
	}
	routed := false
	for _, r := range config.Route.Routes {
		routed = routed || (r.Receiver == "takecare" && len(r.Matchers) == 1 && r.Matchers[0] == `namespace="demo-apps"`)
	}
	if !routed {
		t.Errorf("no route of the demo-apps alerts to the takecare receiver: %+v", config.Route.Routes)
	}
	var credentials string
	for _, r := range config.Receivers {
		if r.Name != "takecare" || len(r.WebhookConfigs) != 1 {
			continue
		}
		w := r.WebhookConfigs[0]
		if w.URL != "http://takecare-alerts."+releaseNamespace+".svc:8080/v1/alerts" || !w.SendResolved || w.HTTPConfig.Authorization.Type != "Bearer" {
			t.Errorf("webhook %+v", w)
		}
		credentials = w.HTTPConfig.Authorization.CredentialsFile
	}
	if path.Base(credentials) != "token" {
		t.Fatalf("credentials_file %q, want the Secret's key token", credentials)
	}
	sts := byName(demo, "StatefulSet", "alertmanager")
	if sts == nil {
		t.Fatal("no alertmanager StatefulSet")
	}
	volumes, _, _ := unstructured.NestedSlice(sts.Object, "spec", "template", "spec", "volumes")
	containers, _, _ := unstructured.NestedSlice(sts.Object, "spec", "template", "spec", "containers")
	var mounts []any
	for _, c := range containers {
		if c := c.(map[string]any); c["name"] == "alertmanager" {
			mounts, _, _ = unstructured.NestedSlice(c, "volumeMounts")
		}
	}
	mounted := false
	for _, m := range mounts {
		m := m.(map[string]any)
		if m["mountPath"] != path.Dir(credentials) {
			continue
		}
		for _, v := range volumes {
			v := v.(map[string]any)
			secret, _, _ := unstructured.NestedString(v, "secret", "secretName")
			mounted = mounted || (v["name"] == m["name"] && secret == tokenSecret)
		}
	}
	if !mounted {
		t.Errorf("Secret %s is not mounted at %s in the alertmanager container", tokenSecret, path.Dir(credentials))
	}
}

// The operator's demo rights (spec 03) come from the chart under
// demo.enabled, by name, in the scenery's namespaces only: production
// renders none, and no operator Role there lies outside the release and
// engine namespaces.
func TestDemoOperatorRoles(t *testing.T) {
	demo := render(t, releaseName, profileArgs("demo")...)
	want := map[string]map[string][]string{ // namespace -> resource -> names
		"demo-apps":  {"deployments": {"billing-api"}, "services": {"billing-api"}},
		"ack-system": {"dbproxies": {"billing-db-proxy"}},
	}
	got := map[string]map[string][]string{}
	for _, o := range ofKind(demo, kindRole) {
		if o.GetName() != "takecare-operator-demo" {
			continue
		}
		got[o.GetNamespace()] = map[string][]string{}
		for _, r := range rulesByRole(t, []*unstructured.Unstructured{o})[kindRole+"/takecare-operator-demo"] {
			if msg := forbidden(r); msg != "" {
				t.Errorf("%s: %s", o.GetNamespace(), msg)
			}
			for _, v := range r.Verbs {
				if !slices.Contains([]string{verbGet, "create", "patch", "delete"}, v) {
					t.Errorf("%s: verb %s on %v", o.GetNamespace(), v, r.Resources)
				}
			}
			if len(r.ResourceNames) == 0 {
				t.Errorf("%s: %v not limited by name", o.GetNamespace(), r.Resources)
			}
			for _, res := range r.Resources {
				got[o.GetNamespace()][res] = r.ResourceNames
			}
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("demo Roles %v, want %v", got, want)
	}

	prod := render(t, releaseName, append(profileArgs("prod"), prodSets...)...)
	for _, o := range ofKind(prod, kindRole) {
		if o.GetName() == "takecare-operator-demo" {
			t.Errorf("prod renders the demo Role in %s", o.GetNamespace())
		}
		if strings.HasPrefix(o.GetName(), "takecare-operator") && o.GetNamespace() != releaseNamespace && o.GetNamespace() != engineNamespace {
			t.Errorf("prod renders the operator Role %s in %s", o.GetName(), o.GetNamespace())
		}
	}
	for _, o := range ofKind(prod, "Deployment") {
		if o.GetName() != "takecare-operator" {
			continue
		}
		containers, _, _ := unstructured.NestedSlice(o.Object, "spec", "template", "spec", "containers")
		args, _, _ := unstructured.NestedStringSlice(containers[0].(map[string]any), "args")
		if slices.Contains(args, "--demo") {
			t.Error("prod runs the demo controller")
		}
	}
}
