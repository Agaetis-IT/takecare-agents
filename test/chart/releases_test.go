// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package chart

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	releasesName      = "takecare-addon-releases"
	releasesConnector = "releases"
	releasesHeader    = "addons:\n  releases:\n    enabled: true\n"
	releasesEgress    = kindNetworkPolicy + "/" + releasesName + "-egress"
	wantURLGuard      = "https, port 443 only"
)

// releasesValues writes an addons.releases block over a public registry
// with its token host, a private Harbor with a robot account, a GitHub
// project and endoflife.date, plus extra lines.
func releasesValues(t *testing.T, extra string) []string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "releases.yaml")
	values := releasesHeader + `    sources:
      - {name: hub, kind: oci, url: "https://registry-1.docker.io", authHost: auth.docker.io}
      - name: harbor
        kind: oci
        url: "https://harbor.example.org"
        tokenSecretRef: {name: harbor-robot, key: secret, username: "robot$takecare"}
      - name: github
        kind: github
        url: "https://api.github.com"
        tokenSecretRef: {name: gh-read}
        components: [{match: kagent, name: kagent-dev/kagent}]
      - {name: eol, kind: endoflife, url: "https://endoflife.date/api/v1", components: [{match: kubernetes, name: kubernetes}]}
` + extra
	if err := os.WriteFile(p, []byte(values), 0o600); err != nil {
		t.Fatal(err)
	}
	return append(prodArgs(), "-f", p)
}

func releasesObjects(objs []*unstructured.Unstructured) map[string]*unstructured.Unstructured {
	out := map[string]*unstructured.Unstructured{}
	for _, o := range objs {
		if strings.HasPrefix(o.GetName(), releasesName) || (o.GetKind() == kindConnector && o.GetName() == releasesConnector) {
			out[o.GetKind()+"/"+o.GetName()] = o
		}
	}
	return out
}

// The releases add-on (spec 12 §7.1): no RBAC and no ServiceAccount
// token, the configuration names where each token is mounted, each token
// Secret mounted with its one key on this pod only, egress DNS and 443
// only, a connector of type releases with no content tool. Off by
// default.
func TestReleasesAddon(t *testing.T) {
	objs := render(t, releaseName, releasesValues(t, "")...)
	got := releasesObjects(objs)
	kinds := make([]string, 0, len(got))
	for k := range got {
		kinds = append(kinds, k)
	}
	slices.Sort(kinds)
	want := []string{
		"ConfigMap/" + releasesName, "Deployment/" + releasesName, releasesEgress, kindNetworkPolicy + "/" + releasesName + "-ingress",
		"Service/" + releasesName, kindSA + "/" + releasesName, kindConnector + "/" + releasesConnector,
	}
	if diff := cmp.Diff(want, kinds); diff != "" {
		t.Fatalf("the releases add-on's objects (-want +got):\n%s", diff)
	}
	for _, o := range objs {
		if k := o.GetKind(); k == kindRole || k == kindClusterRole || k == kindRoleBinding || k == kindClusterBinding {
			if strings.Contains(o.GetName(), "releases") || bindsSA(o, releasesName) {
				t.Errorf("the releases add-on is given RBAC: %s %s", k, o.GetName())
			}
		}
	}
	// The read-only guard over a demo render with the add-on on.
	vals := releasesValues(t, "")
	checkAddonRBAC(t, render(t, releaseName, append(profileArgs("demo"), vals[len(vals)-2:]...)...))
	if mount, found, _ := unstructured.NestedBool(got[kindSA+"/"+releasesName].Object, "automountServiceAccountToken"); !found || mount {
		t.Error("the releases add-on's ServiceAccount mounts its token")
	}

	pod, _, _ := unstructured.NestedMap(got["Deployment/"+releasesName].Object, "spec", "template", "spec")
	if pod["automountServiceAccountToken"] != false {
		t.Errorf("the releases server's pod mounts a token: %v", pod["automountServiceAccountToken"])
	}
	container := pod["containers"].([]any)[0].(map[string]any)
	args, _ := json.Marshal(container["args"])
	if string(args) != `["--listen=:8080","--config-file=/etc/takecare/releases/config/config.json"]` {
		t.Errorf("args = %s", args)
	}
	sc, _ := json.Marshal(container["securityContext"])
	if string(sc) != `{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]},"readOnlyRootFilesystem":true}` {
		t.Errorf("securityContext = %s", sc)
	}
	volumes, _ := json.Marshal(pod["volumes"])
	for _, v := range []string{
		`{"name":"token-1","secret":{"items":[{"key":"secret","path":"token"}],"secretName":"harbor-robot"}}`,
		`{"name":"token-2","secret":{"items":[{"key":"token","path":"token"}],"secretName":"gh-read"}}`,
	} {
		if !strings.Contains(string(volumes), v) {
			t.Errorf("volumes %s lack %s", volumes, v)
		}
	}
	// The token Secrets are mounted on this pod only.
	for _, d := range ofKind(objs, "Deployment") {
		if d.GetName() == releasesName {
			continue
		}
		raw, _ := json.Marshal(d.Object)
		for _, secret := range []string{"harbor-robot", "gh-read"} {
			if strings.Contains(string(raw), `"`+secret+`"`) {
				t.Errorf("Deployment %s names the token Secret %s", d.GetName(), secret)
			}
		}
	}

	checkReleasesConfig(t, got["ConfigMap/"+releasesName])

	egress, _ := json.Marshal(got[releasesEgress].Object["spec"].(map[string]any)["egress"])
	wantEgress := `[{"ports":[{"port":53,"protocol":"UDP"},{"port":53,"protocol":"TCP"}],"to":[{"namespaceSelector":{"matchLabels":{"kubernetes.io/metadata.name":"kube-system"}},"podSelector":{"matchLabels":{"k8s-app":"kube-dns"}}}]},` +
		`{"ports":[{"port":443,"protocol":"TCP"}],"to":[{"ipBlock":{"cidr":"0.0.0.0/0","except":["10.0.0.0/8","172.16.0.0/12","192.168.0.0/16","169.254.0.0/16"]}}]}]`
	if string(egress) != wantEgress {
		t.Errorf("egress = %s\nwant %s", egress, wantEgress)
	}
	spec, _ := json.Marshal(got[kindConnector+"/"+releasesConnector].Object["spec"])
	if string(spec) != `{"description":"Published versions and release cycles, read by the Mécano's collector (read-only)","mcp":{"addon":"releases"},"types":["releases"]}` {
		t.Errorf("the releases connector = %s", spec)
	}
}

// The egress follows the mirror's CIDRs or the proxy when set; the add-on
// is off by default.
func TestReleasesAddonEgress(t *testing.T) {
	t.Run("to the mirror's CIDRs", func(t *testing.T) {
		raw, _ := json.Marshal(releasesObjects(render(t, releaseName, releasesValues(t, "    egressCIDRs: [10.20.0.0/16, 192.168.4.10/32]\n")...))[releasesEgress].Object["spec"])
		if !strings.Contains(string(raw), `{"ports":[{"port":443,"protocol":"TCP"}],"to":[{"ipBlock":{"cidr":"10.20.0.0/16"}},{"ipBlock":{"cidr":"192.168.4.10/32"}}]}`) ||
			strings.Contains(string(raw), "0.0.0.0/0") {
			t.Errorf("egress to the CIDRs = %s", raw)
		}
	})
	t.Run("through the proxy", func(t *testing.T) {
		raw, _ := json.Marshal(releasesObjects(render(t, releaseName, releasesValues(t, "    egress:\n      proxy: {namespace: proxy, podSelector: {app: squid}, port: 3128}\n")...))[releasesEgress].Object["spec"])
		if strings.Contains(string(raw), "ipBlock") || !strings.Contains(string(raw), `"port":3128`) {
			t.Errorf("egress through the proxy = %s", raw)
		}
	})
	t.Run("off by default", func(t *testing.T) {
		for _, profile := range profiles {
			if got := releasesObjects(render(t, releaseName, append(profileArgs(profile), prodSets...)...)); len(got) != 0 {
				t.Errorf("%s: addons.releases is off, yet %v render", profile, got)
			}
		}
	})
}

// checkReleasesConfig compares the server's configuration with the values:
// where each token is mounted and its Secret's name, never the token.
func checkReleasesConfig(t *testing.T, cm *unstructured.Unstructured) {
	t.Helper()
	data, _, _ := unstructured.NestedString(cm.Object, "data", "config.json")
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(data)); err != nil {
		t.Fatalf("config.json: %v\n%s", err, data)
	}
	want := `{"cache":{"maxEntries":512,"refresh":"24h","staleMax":"168h"},"sources":[` +
		`{"authHost":"auth.docker.io","components":[],"kind":"oci","name":"hub","url":"https://registry-1.docker.io"},` +
		`{"components":[],"kind":"oci","name":"harbor","tokenFile":"/etc/takecare/releases/token-1/token","tokenSecret":"harbor-robot","url":"https://harbor.example.org","username":"robot$takecare"},` +
		`{"components":[{"match":"kagent","name":"kagent-dev/kagent"}],"kind":"github","name":"github","tokenFile":"/etc/takecare/releases/token-2/token","tokenSecret":"gh-read","url":"https://api.github.com"},` +
		`{"components":[{"match":"kubernetes","name":"kubernetes"}],"kind":"endoflife","name":"eol","url":"https://endoflife.date/api/v1"}]}`
	if compact.String() != want {
		t.Errorf("config.json = %s\nwant %s", compact.String(), want)
	}
}

func TestReleasesAddonGuards(t *testing.T) {
	var many strings.Builder
	for i := range 33 {
		fmt.Fprintf(&many, "      - {name: s%d, kind: oci, url: \"https://r.example.com\"}\n", i)
	}
	const src = "      - {name: s, kind: "
	for name, tc := range map[string]struct {
		values, want string
	}{
		"no source":                  {"    sources: []\n", "addons.releases.sources is REQUIRED"},
		"33 sources":                 {"    sources:\n" + many.String(), "addons.releases.sources is REQUIRED"},
		"a bad name":                 {"    sources:\n      - {name: Hub, kind: oci, url: \"https://r.example.com\"}\n", "sources[].name is REQUIRED"},
		"a source twice":             {"    sources:\n" + src + "oci, url: \"https://a.example.com\"}\n" + src + "oci, url: \"https://b.example.com\"}\n", "s is listed twice"},
		"an unknown kind":            {"    sources:\n" + src + "rss, url: \"https://r.example.com\"}\n", "kind is REQUIRED"},
		"plain http":                 {"    sources:\n" + src + "oci, url: \"http://r.example.com\"}\n", wantURLGuard},
		"another port":               {"    sources:\n" + src + "oci, url: \"https://r.example.com:5000\"}\n", wantURLGuard},
		"a query":                    {"    sources:\n" + src + "helm, url: \"https://r.example.com/?x=1\"}\n", wantURLGuard},
		"a helm source on oci://":    {"    sources:\n" + src + "helm, url: \"oci://r.example.com/charts\"}\n", "use an oci source"},
		"user information":           {"    sources:\n" + src + "helm, url: \"https://reader@r.example.com\"}\n", "never the URL"},
		"authHost on helm":           {"    sources:\n" + src + "helm, url: \"https://r.example.com\", authHost: auth.example.com}\n", "authHost"},
		"a username without secret":  {"    sources:\n" + src + "oci, url: \"https://r.example.com\", tokenSecretRef: {username: robot}}\n", "goes with tokenSecretRef.name"},
		"a username on gitlab":       {"    sources:\n" + src + "gitlab, url: \"https://gitlab.com/api/v4\", tokenSecretRef: {name: t, username: u}, components: [{match: a, name: g/p}]}\n", "on an oci or helm source"},
		"github without components":  {"    sources:\n" + src + "github, url: \"https://api.github.com\"}\n", "components is REQUIRED"},
		"a component without a name": {"    sources:\n" + src + "endoflife, url: \"https://endoflife.date/api/v1\", components: [{match: k}]}\n", "needs a match and a name"},
		"CIDRs and a proxy":          {"    sources:\n" + src + "oci, url: \"https://r.example.com\"}\n    egressCIDRs: [10.0.0.0/8]\n    egress:\n      proxy: {namespace: p, podSelector: {app: p}, port: 3128}\n", "exclusive: one way out"},
	} {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "releases.yaml")
			if err := os.WriteFile(p, []byte(releasesHeader+tc.values), 0o600); err != nil {
				t.Fatal(err)
			}
			out, err := helmTemplate(t, releaseName, releaseNamespace, append(prodArgs(), "-f", p)...).CombinedOutput()
			if err == nil || !strings.Contains(string(out), tc.want) {
				t.Errorf("err %v, output does not say %q:\n%s", err, tc.want, out)
			}
		})
	}
}
