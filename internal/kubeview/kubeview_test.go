// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package kubeview

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// Spec 13 §6: the read-only client exposes get and list, nothing else.
func TestReaderMethodSet(t *testing.T) {
	var got []string
	typ := reflect.TypeFor[*Reader]()
	for i := range typ.NumMethod() {
		got = append(got, typ.Method(i).Name)
	}
	slices.Sort(got)
	if diff := cmp.Diff([]string{"Get", "List"}, got); diff != "" {
		t.Errorf("*Reader's exported methods (-want +got):\n%s", diff)
	}
	// Its only field holds a client.Reader: a writer is not reachable
	// without a type assertion, which the package never makes.
	field, ok := reflect.TypeFor[Reader]().FieldByName("r")
	if !ok || field.Type.String() != "client.Reader" {
		t.Errorf("Reader.r is %v, want a client.Reader", field.Type)
	}
}

// The transport refuses every verb but GET, and a watch, before the
// request leaves the process.
func TestGetOnly(t *testing.T) {
	var reached []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = append(reached, r.Method+" "+r.URL.RequestURI())
	}))
	t.Cleanup(srv.Close)
	rt := getOnly(http.DefaultTransport)
	for _, tc := range []struct {
		method, path string
		allowed      bool
	}{
		{http.MethodGet, "/apis/takecare.agaetis.fr/v1alpha1/takecaredocs", true},
		{http.MethodGet, "/apis/takecare.agaetis.fr/v1alpha1/takecaredocs?watch=true", false},
		{http.MethodGet, "/apis/takecare.agaetis.fr/v1alpha1/takecaredocs?watch=1", false},
		{http.MethodPost, "/apis/takecare.agaetis.fr/v1alpha1/namespaces/x/takecaresurveys", false},
		{http.MethodPut, "/api/v1/namespaces/x/configmaps/y", false},
		{http.MethodPatch, "/api/v1/namespaces/x/configmaps/y", false},
		{http.MethodDelete, "/api/v1/namespaces/x/configmaps/y", false},
		{http.MethodHead, "/api/v1", false},
		{http.MethodOptions, "/api/v1", false},
		{"CONNECT", "/api/v1/namespaces/x/pods/y/exec", false},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			reached = nil
			req, err := http.NewRequestWithContext(t.Context(), tc.method, srv.URL+tc.path, http.NoBody)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := rt.RoundTrip(req)
			if resp != nil {
				_ = resp.Body.Close()
			}
			if tc.allowed != (err == nil) {
				t.Errorf("err = %v, want allowed %t", err, tc.allowed)
			}
			if tc.allowed != (len(reached) == 1) {
				t.Errorf("the server saw %v, want allowed %t", reached, tc.allowed)
			}
		})
	}
}

// Load follows the loading rules, applies --context for the run only, and
// writes nothing: the file is unchanged and no file appears beside it.
func TestLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	cfg := clientcmdapi.NewConfig()
	cfg.Clusters["a"] = &clientcmdapi.Cluster{Server: "https://a.example:6443"}
	cfg.Clusters["b"] = &clientcmdapi.Cluster{Server: "https://b.example:6443"}
	cfg.AuthInfos["u"] = &clientcmdapi.AuthInfo{Token: "t"}
	cfg.Contexts["a"] = &clientcmdapi.Context{Cluster: "a", AuthInfo: "u"}
	cfg.Contexts["b"] = &clientcmdapi.Context{Cluster: "b", AuthInfo: "u"}
	cfg.CurrentContext = "a"
	if err := clientcmd.WriteToFile(*cfg, path); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// KUBECONFIG must not win over the explicit path.
	t.Setenv("KUBECONFIG", filepath.Join(dir, "absent"))

	for _, tc := range []struct{ context, want string }{{"", "https://a.example:6443"}, {"b", "https://b.example:6443"}} {
		got, err := Load(path, tc.context)
		if err != nil {
			t.Fatal(err)
		}
		if got.Host != tc.want {
			t.Errorf("context %q: host = %q, want %q", tc.context, got.Host, tc.want)
		}
	}
	if _, err := Load(path, "absent"); err == nil {
		t.Error("an unknown context loaded")
	}
	if _, err := Load(filepath.Join(dir, "absent"), ""); err == nil {
		t.Error("a missing explicit kubeconfig loaded")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(string(before), string(after)); diff != "" {
		t.Errorf("the kubeconfig changed (-before +after):\n%s", diff)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("files beside the kubeconfig: %v, want only config", entries)
	}
}

// With no explicit path, KUBECONFIG is read.
func TestLoadFromEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	cfg := clientcmdapi.NewConfig()
	cfg.Clusters["a"] = &clientcmdapi.Cluster{Server: "https://env.example:6443"}
	cfg.AuthInfos["u"] = &clientcmdapi.AuthInfo{Token: "t"}
	cfg.Contexts["a"] = &clientcmdapi.Context{Cluster: "a", AuthInfo: "u"}
	cfg.CurrentContext = "a"
	if err := clientcmd.WriteToFile(*cfg, path); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", path)
	got, err := Load("", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Host != "https://env.example:6443" {
		t.Errorf("host = %q, want the KUBECONFIG cluster", got.Host)
	}
}
