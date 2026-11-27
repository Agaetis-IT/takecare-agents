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
	proposeName      = "takecare-addon-code-propose"
	proposeConnector = "code-propose"
	wantSegment      = "without a . or .. segment"
	proposeHeader    = "addons:\n  codePropose:\n    enabled: true\n    tokenSecret: {name: forge-tokens}\n"
)

// proposeValues writes an addons.codePropose block over a GitHub and a
// GitLab repository, plus extra lines.
func proposeValues(t *testing.T, extra string) []string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "propose.yaml")
	values := proposeHeader + `    repositories:
      - {forge: github, apiURL: "https://api.github.com", repository: acme/shop-deploy, tokenKey: github-shop}
      - {forge: gitlab, apiURL: "https://gitlab.example.org/api/v4", repository: ops/platform/deploy, tokenKey: gitlab-platform}
` + extra
	if err := os.WriteFile(p, []byte(values), 0o600); err != nil {
		t.Fatal(err)
	}
	return append(prodArgs(), "-f", p)
}

func proposeObjects(objs []*unstructured.Unstructured) map[string]*unstructured.Unstructured {
	out := map[string]*unstructured.Unstructured{}
	for _, o := range objs {
		if strings.HasPrefix(o.GetName(), proposeName) || (o.GetKind() == kindConnector && o.GetName() == proposeConnector) {
			out[o.GetKind()+"/"+o.GetName()] = o
		}
	}
	return out
}

// The propose add-on (spec 12 §6): no RBAC and no token, the write tokens
// mounted on its pod only, each its one key, the propose override on its
// connector, egress to 443 only. Off by default.
func TestCodeProposeAddon(t *testing.T) {
	objs := render(t, releaseName, proposeValues(t, "")...)
	got := proposeObjects(objs)
	kinds := make([]string, 0, len(got))
	for k := range got {
		kinds = append(kinds, k)
	}
	slices.Sort(kinds)
	want := []string{
		"ConfigMap/" + proposeName, "Deployment/" + proposeName, kindNetworkPolicy + "/" + proposeName + "-egress", kindNetworkPolicy + "/" + proposeName + "-ingress",
		"Service/" + proposeName, kindSA + "/" + proposeName, kindConnector + "/" + proposeConnector,
	}
	if diff := cmp.Diff(want, kinds); diff != "" {
		t.Fatalf("the propose add-on's objects (-want +got):\n%s", diff)
	}
	for _, o := range objs {
		if k := o.GetKind(); k == kindRole || k == kindClusterRole || k == kindRoleBinding || k == kindClusterBinding {
			if strings.Contains(o.GetName(), "code-propose") || bindsSA(o, proposeName) {
				t.Errorf("the propose add-on is given RBAC: %s %s", k, o.GetName())
			}
		}
	}
	if mount, found, _ := unstructured.NestedBool(got[kindSA+"/"+proposeName].Object, "automountServiceAccountToken"); !found || mount {
		t.Error("the propose add-on's ServiceAccount mounts its token")
	}
	pod, _, _ := unstructured.NestedMap(got["Deployment/"+proposeName].Object, "spec", "template", "spec")
	if pod["automountServiceAccountToken"] != false {
		t.Errorf("the propose server's pod mounts a token: %v", pod["automountServiceAccountToken"])
	}
	args, _ := json.Marshal(pod["containers"].([]any)[0].(map[string]any)["args"])
	if string(args) != `["--mode=propose","--listen=:8080","--repositories-file=/etc/takecare/propose/config/repositories.json"]` {
		t.Errorf("args = %s", args)
	}
	volumes, _ := json.Marshal(pod["volumes"])
	if !strings.Contains(string(volumes), `{"name":"tokens","secret":{"items":[{"key":"github-shop","path":"0"},{"key":"gitlab-platform","path":"1"}],"secretName":"forge-tokens"}}`) {
		t.Errorf("volumes = %s", volumes)
	}

	data, _, _ := unstructured.NestedString(got["ConfigMap/"+proposeName].Object, "data", "repositories.json")
	var repos []map[string]string
	if err := json.Unmarshal([]byte(data), &repos); err != nil {
		t.Fatalf("repositories.json: %v\n%s", err, data)
	}
	wantRepos := []map[string]string{
		{"forge": "github", "apiURL": "https://api.github.com", "repository": "acme/shop-deploy", "tokenFile": "/etc/takecare/propose/tokens/0"},               //nolint:gosec // a mounted file's path, no credential
		{"forge": "gitlab", "apiURL": "https://gitlab.example.org/api/v4", "repository": "ops/platform/deploy", "tokenFile": "/etc/takecare/propose/tokens/1"}, //nolint:gosec // a mounted file's path, no credential
	}
	if diff := cmp.Diff(wantRepos, repos); diff != "" {
		t.Errorf("repositories.json (-want +got):\n%s", diff)
	}

	spec, _ := json.Marshal(got[kindConnector+"/"+proposeConnector].Object["spec"])
	if string(spec) != `{"description":"Draft pull requests on the customer's listed repositories (propose)","mcp":{"addon":"code-propose"},`+
		`"propose":{"repositories":[{"apiURL":"https://api.github.com","forge":"github","repository":"acme/shop-deploy"},{"apiURL":"https://gitlab.example.org/api/v4","forge":"gitlab","repository":"ops/platform/deploy"}],"tokenSecretRef":{"name":"forge-tokens"}},`+
		`"tools":[{"class":"propose","name":"propose_change"}],"types":["code"]}` {
		t.Errorf("the propose connector = %s", spec)
	}

	// The write tokens are mounted on the propose pod only.
	for _, d := range ofKind(objs, "Deployment") {
		if d.GetName() == proposeName {
			continue
		}
		raw, _ := json.Marshal(d.Object["spec"])
		if strings.Contains(string(raw), "forge-tokens") {
			t.Errorf("Deployment %s mounts the write tokens", d.GetName())
		}
	}

	egress, _ := json.Marshal(got[kindNetworkPolicy+"/"+proposeName+"-egress"].Object["spec"].(map[string]any)["egress"])
	if !strings.Contains(string(egress), `"ports":[{"port":443,"protocol":"TCP"}],"to":[{"ipBlock"`) || strings.Contains(string(egress), `"port":22`) {
		t.Errorf("egress = %s, want DNS and 443 only", egress)
	}
	t.Run("through the proxy", func(t *testing.T) {
		raw, _ := json.Marshal(proposeObjects(render(t, releaseName, proposeValues(t, "    egress:\n      proxy: {namespace: proxy, podSelector: {app: squid}, port: 3128}\n")...))[kindNetworkPolicy+"/"+proposeName+"-egress"].Object["spec"])
		if strings.Contains(string(raw), "ipBlock") || !strings.Contains(string(raw), `"port":3128`) {
			t.Errorf("egress through the proxy = %s", raw)
		}
	})
	t.Run("off by default", func(t *testing.T) {
		for _, profile := range profiles {
			if got := proposeObjects(render(t, releaseName, append(profileArgs(profile), prodSets...)...)); len(got) != 0 {
				t.Errorf("%s: addons.codePropose is off, yet %v render", profile, got)
			}
		}
	})
}

func TestCodeProposeAddonGuards(t *testing.T) {
	var many strings.Builder
	for i := range 17 {
		fmt.Fprintf(&many, "      - {forge: github, apiURL: https://api.github.com, repository: acme/r%d, tokenKey: k%d}\n", i, i)
	}
	repo := func(fields string) string { return "    repositories:\n      - {" + fields + "}\n" }
	gh := "forge: github, apiURL: https://api.github.com, tokenKey: k"
	for name, tc := range map[string]struct {
		values, want string
	}{
		"no token Secret":       {"addons:\n  codePropose:\n    enabled: true\n" + repo(gh+", repository: acme/shop"), "tokenSecret.name is REQUIRED"},
		"no repository":         {proposeHeader + "    repositories: []\n", "addons.codePropose.repositories is REQUIRED"},
		"17 repositories":       {proposeHeader + "    repositories:\n" + many.String(), "addons.codePropose.repositories is REQUIRED"},
		"a .. segment":          {proposeHeader + repo(gh+", repository: acme/.."), wantSegment},
		"a .git suffix":         {proposeHeader + repo(gh+", repository: acme/shop.git"), wantSegment},
		"no slash":              {proposeHeader + repo(gh+", repository: shop"), wantSegment},
		"a subgroup on GitHub":  {proposeHeader + repo(gh+", repository: acme/sub/shop"), "owner/name on GitHub"},
		"an unknown forge":      {proposeHeader + repo("forge: gitea, apiURL: https://x, tokenKey: k, repository: acme/shop"), "forge is REQUIRED"},
		"an http API":           {proposeHeader + repo("forge: github, apiURL: http://api.github.com, tokenKey: k, repository: acme/shop"), "apiURL is REQUIRED"},
		"a user in the API URL": {proposeHeader + repo("forge: github, apiURL: https://u@api.github.com, tokenKey: k, repository: acme/shop"), "apiURL is REQUIRED"},
		"no token key":          {proposeHeader + repo("forge: github, apiURL: https://api.github.com, repository: acme/shop"), "tokenKey is REQUIRED"},
		"a token key with /":    {proposeHeader + repo("forge: github, apiURL: https://api.github.com, tokenKey: a/b, repository: acme/shop"), "tokenKey is REQUIRED"},
		"a repository twice":    {proposeHeader + "    repositories:\n      - {" + gh + "1, repository: acme/shop}\n      - {" + gh + "2, repository: acme/shop}\n", "acme/shop is listed twice"},
		"a token key twice":     {proposeHeader + "    repositories:\n      - {" + gh + ", repository: acme/a}\n      - {" + gh + ", repository: acme/b}\n", "tokenKey k is used twice"},
	} {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "propose.yaml")
			if err := os.WriteFile(p, []byte(tc.values), 0o600); err != nil {
				t.Fatal(err)
			}
			out, err := helmTemplate(t, releaseName, releaseNamespace, append(prodArgs(), "-f", p)...).CombinedOutput()
			if err == nil || !strings.Contains(string(out), tc.want) {
				t.Errorf("err %v, output does not say %q:\n%s", err, tc.want, out)
			}
		})
	}
}

// A token key that YAML would read as a number or a boolean stays a
// string in the Secret volume's items.
func TestCodeProposeAddonTokenKeyQuoted(t *testing.T) {
	p := filepath.Join(t.TempDir(), "propose.yaml")
	values := proposeHeader + `    repositories:
      - {forge: github, apiURL: "https://api.github.com", repository: acme/a, tokenKey: "1234"}
      - {forge: github, apiURL: "https://api.github.com", repository: acme/b, tokenKey: "true"}
`
	if err := os.WriteFile(p, []byte(values), 0o600); err != nil {
		t.Fatal(err)
	}
	got := proposeObjects(render(t, releaseName, append(prodArgs(), "-f", p)...))
	volumes, _, _ := unstructured.NestedSlice(got["Deployment/"+proposeName].Object, "spec", "template", "spec", "volumes")
	raw, _ := json.Marshal(volumes)
	if !strings.Contains(string(raw), `"items":[{"key":"1234","path":"0"},{"key":"true","path":"1"}]`) {
		t.Errorf("the token keys are not strings: %s", raw)
	}
}
