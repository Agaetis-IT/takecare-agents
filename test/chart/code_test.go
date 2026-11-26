// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package chart

import (
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
	codeName      = "takecare-addon-code"
	codeConnector = "code"
	kindConnector = "TakeCareConnector"
	wantSSHGuard  = "an SSH remote needs sshKeySecret and knownHostsConfigMap"
)

// codeValues writes an addons.code block over two repositories, an HTTPS
// one with a token and an SSH one with a deploy key, plus extra lines.
func codeValues(t *testing.T, extra string) []string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "code.yaml")
	values := `addons:
  code:
    enabled: true
    repositories:
      - name: shop
        url: https://github.com/acme/shop-deploy.git
        credentialSecret: {name: git-shop, key: pat}
      - name: platform
        url: git@gitlab.example.org:ops/platform.git
        ref: release
        sshKeySecret: {name: git-platform}
        knownHostsConfigMap: {name: git-hosts}
` + extra
	if err := os.WriteFile(p, []byte(values), 0o600); err != nil {
		t.Fatal(err)
	}
	return append(prodArgs(), "-f", p)
}

// prodArgs renders the prod profile with its REQUIRED values filled.
func prodArgs() []string { return append(profileArgs("prod"), prodSets...) }

func codeObjects(objs []*unstructured.Unstructured) map[string]*unstructured.Unstructured {
	out := map[string]*unstructured.Unstructured{}
	for _, o := range objs {
		if strings.HasPrefix(o.GetName(), codeName) || (o.GetKind() == kindConnector && o.GetName() == codeConnector) {
			out[o.GetKind()+"/"+o.GetName()] = o
		}
	}
	return out
}

// The code add-on (spec 12 §5): no Role and no token, the repositories
// file names where each credential is mounted, each Secret mounted with
// its one key, egress to 443 and 22 for an SSH remote, and the connector
// marks file_get as content. Off by default.
func TestCodeAddon(t *testing.T) {
	objs := render(t, releaseName, codeValues(t, "")...)
	got := codeObjects(objs)
	kinds := make([]string, 0, len(got))
	for k := range got {
		kinds = append(kinds, k)
	}
	slices.Sort(kinds)
	want := []string{
		"ConfigMap/" + codeName, "Deployment/" + codeName, kindNetworkPolicy + "/" + codeName + "-egress", kindNetworkPolicy + "/" + codeName + "-ingress",
		"Service/" + codeName, kindSA + "/" + codeName, kindConnector + "/" + codeConnector,
	}
	if diff := cmp.Diff(want, kinds); diff != "" {
		t.Fatalf("the code add-on's objects (-want +got):\n%s", diff)
	}
	for _, o := range objs {
		if k := o.GetKind(); k == kindRole || k == kindClusterRole || k == kindRoleBinding || k == kindClusterBinding {
			if strings.Contains(o.GetName(), "addon-code") || bindsSA(o, codeName) {
				t.Errorf("the code add-on is given RBAC: %s %s", k, o.GetName())
			}
		}
	}
	if mount, found, _ := unstructured.NestedBool(got[kindSA+"/"+codeName].Object, "automountServiceAccountToken"); !found || mount {
		t.Error("the code add-on's ServiceAccount mounts its token")
	}

	pod, _, _ := unstructured.NestedMap(got["Deployment/"+codeName].Object, "spec", "template", "spec")
	if pod["automountServiceAccountToken"] != false {
		t.Errorf("the code server's pod mounts a token: %v", pod["automountServiceAccountToken"])
	}
	containers := pod["containers"].([]any)
	args, _ := json.Marshal(containers[0].(map[string]any)["args"])
	if string(args) != `["--mode=read","--listen=:8080","--repositories-file=/etc/takecare/code/config/repositories.json"]` {
		t.Errorf("args = %s", args)
	}
	volumes, _ := json.Marshal(pod["volumes"])
	for _, v := range []string{
		`{"name":"cred-0","secret":{"items":[{"key":"pat","path":"token"}],"secretName":"git-shop"}}`,
		`{"name":"ssh-1","secret":{"items":[{"key":"id_ed25519","path":"key"}],"secretName":"git-platform"}}`,
		`{"configMap":{"items":[{"key":"known_hosts","path":"known_hosts"}],"name":"git-hosts"},"name":"hosts-1"}`,
	} {
		if !strings.Contains(string(volumes), v) {
			t.Errorf("volumes %s lack %s", volumes, v)
		}
	}

	data, _, _ := unstructured.NestedString(got["ConfigMap/"+codeName].Object, "data", "repositories.json")
	var repos []map[string]string
	if err := json.Unmarshal([]byte(data), &repos); err != nil {
		t.Fatalf("repositories.json: %v\n%s", err, data)
	}
	wantRepos := []map[string]string{
		{"name": "shop", "url": "https://github.com/acme/shop-deploy.git", "ref": "main", "pollInterval": "5m", //nolint:gosec // a mounted file's path, no credential
			"credentialFile": "/etc/takecare/code/cred-0/token", "credentialUsername": "x-access-token"},
		{"name": "platform", "url": "git@gitlab.example.org:ops/platform.git", "ref": "release", "pollInterval": "5m",
			"sshKeyFile": "/etc/takecare/code/ssh-1/key", "knownHostsFile": "/etc/takecare/code/hosts-1/known_hosts"},
	}
	if diff := cmp.Diff(wantRepos, repos); diff != "" {
		t.Errorf("repositories.json (-want +got):\n%s", diff)
	}

	egress, _ := json.Marshal(got[kindNetworkPolicy+"/"+codeName+"-egress"].Object["spec"].(map[string]any)["egress"])
	if !strings.Contains(string(egress), `"ports":[{"port":443,"protocol":"TCP"},{"port":22,"protocol":"TCP"}]`) {
		t.Errorf("egress with an SSH remote = %s, want 443 and 22", egress)
	}
	spec, _ := json.Marshal(got[kindConnector+"/"+codeConnector].Object["spec"])
	if string(spec) != `{"description":"The customer's Git: manifests, charts and GitOps definitions (read-only)","mcp":{"addon":"code"},"tools":[{"content":true,"name":"file_get"}],"types":["code"]}` {
		t.Errorf("the code connector = %s", spec)
	}

	t.Run("HTTPS only: no port 22", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "https.yaml")
		if err := os.WriteFile(p, []byte("addons:\n  code:\n    enabled: true\n    repositories:\n      - {name: shop, url: https://github.com/acme/shop-deploy.git}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		np := codeObjects(render(t, releaseName, append(prodArgs(), "-f", p)...))[kindNetworkPolicy+"/"+codeName+"-egress"]
		raw, _ := json.Marshal(np.Object["spec"])
		if strings.Contains(string(raw), `"port":22`) || !strings.Contains(string(raw), `"port":443`) {
			t.Errorf("egress = %s", raw)
		}
	})
	t.Run("through the proxy", func(t *testing.T) {
		objs := render(t, releaseName, codeValues(t, "    egress:\n      proxy: {namespace: proxy, podSelector: {app: squid}, port: 3128}\n")...)
		raw, _ := json.Marshal(codeObjects(objs)[kindNetworkPolicy+"/"+codeName+"-egress"].Object["spec"])
		if strings.Contains(string(raw), "ipBlock") || !strings.Contains(string(raw), `"port":3128`) {
			t.Errorf("egress through the proxy = %s", raw)
		}
	})
	t.Run("off by default", func(t *testing.T) {
		for _, profile := range profiles {
			if got := codeObjects(render(t, releaseName, append(profileArgs(profile), prodSets...)...)); len(got) != 0 {
				t.Errorf("%s: addons.code is off, yet %v render", profile, got)
			}
		}
	})
}

func bindsSA(o *unstructured.Unstructured, sa string) bool {
	subjects, _, _ := unstructured.NestedSlice(o.Object, "subjects")
	return slices.ContainsFunc(subjects, func(s any) bool { return s.(map[string]any)["name"] == sa })
}

func TestCodeAddonGuards(t *testing.T) {
	var many strings.Builder
	for i := range 17 {
		fmt.Fprintf(&many, "      - {name: r%d, url: https://git.example.com/r.git}\n", i)
	}
	for name, tc := range map[string]struct {
		repos, want string
	}{
		"no repository":              {"    repositories: []\n", "addons.code.repositories is REQUIRED"},
		"17 repositories":            {"    repositories:\n" + many.String(), "addons.code.repositories is REQUIRED"},
		"a name with a slash":        {"    repositories:\n      - {name: acme/shop, url: https://x}\n", "repositories[].name is REQUIRED"},
		"a repository twice":         {"    repositories:\n      - {name: shop, url: https://x}\n      - {name: shop, url: https://y}\n", "shop is listed twice"},
		"no URL":                     {"    repositories:\n      - {name: shop}\n", "repositories[shop].url is REQUIRED"},
		"an http URL":                {"    repositories:\n      - {name: shop, url: http://git.example.com/shop.git}\n", "no other scheme"},
		"a file URL":                 {"    repositories:\n      - {name: shop, url: file:///srv/shop.git}\n", "no other scheme"},
		"a user in an https URL":     {"    repositories:\n      - {name: shop, url: https://reader@git.example.com/shop.git}\n", "never the URL"},
		"two credentials":            {"    repositories:\n      - {name: shop, url: ssh://git@git.example.com/shop.git, credentialSecret: {name: t}, sshKeySecret: {name: k}, knownHostsConfigMap: {name: h}}\n", "are exclusive"},
		"SSH without a key":          {"    repositories:\n      - {name: shop, url: ssh://git@git.example.com/shop.git}\n", wantSSHGuard},
		"a key without hosts":        {"    repositories:\n      - {name: shop, url: git@git.example.com:shop.git, sshKeySecret: {name: k}}\n", wantSSHGuard},
		"an HTTPS URL with a key":    {"    repositories:\n      - {name: shop, url: https://x, sshKeySecret: {name: k}, knownHostsConfigMap: {name: h}}\n", "goes with an SSH remote"},
		"a password in an SSH URL":   {"    repositories:\n      - {name: shop, url: ssh://git:made-up@git.example.com/shop.git, sshKeySecret: {name: k}, knownHostsConfigMap: {name: h}}\n", "carries a password"},
	} {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "code.yaml")
			if err := os.WriteFile(p, []byte("addons:\n  code:\n    enabled: true\n"+tc.repos), 0o600); err != nil {
				t.Fatal(err)
			}
			out, err := helmTemplate(t, releaseName, releaseNamespace, append(prodArgs(), "-f", p)...).CombinedOutput()
			if err == nil || !strings.Contains(string(out), tc.want) {
				t.Errorf("err %v, output does not say %q:\n%s", err, tc.want, out)
			}
		})
	}
}
