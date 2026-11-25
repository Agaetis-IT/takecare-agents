// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package testenv

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// AddonEnv is an API server with the product's CRDs and two identities:
// Admin for the fixtures, Addon for the add-on under test, which the API
// server authorizes as the add-on's ServiceAccount bound to the RBAC the
// takecare chart renders for it in the demo profile.
type AddonEnv struct {
	Admin, Addon *rest.Config
}

// StartAddon starts an API server and gives the add-on's ServiceAccount
// (takecare-addon-<name>) the Roles, ClusterRoles and bindings of
// templates/addons/<name>.yaml, as an installation gets them.
func StartAddon(t *testing.T, name string) AddonEnv {
	t.Helper()
	env, admin := start(t, false)
	c, err := client.New(admin, client.Options{})
	if err != nil {
		t.Fatal(err)
	}
	rbac := addonRBAC(t, name)
	createNamespaces(t, c, rbac)
	for _, obj := range rbac {
		if err := c.Create(context.Background(), obj); err != nil {
			t.Fatalf("create %s %s: %v", obj.GetKind(), obj.GetName(), err)
		}
	}
	user := "system:serviceaccount:" + OperatorNamespace + ":takecare-addon-" + name
	groups := []string{"system:serviceaccounts", "system:serviceaccounts:" + OperatorNamespace, "system:authenticated"}
	added, err := env.AddUser(envtest.User{Name: user, Groups: groups}, &rest.Config{QPS: admin.QPS, Burst: admin.Burst})
	if err != nil {
		t.Fatalf("add the add-on's user: %v", err)
	}
	checks := rightChecks(t, rbac)
	for i := range checks {
		checks[i].user, checks[i].groups = user, groups
	}
	if err := awaitRights(context.Background(), c, checks, rightsTimeout); err != nil {
		t.Fatal(err)
	}
	return AddonEnv{Admin: admin, Addon: added.Config()}
}

// addonRBAC renders an add-on's RBAC objects from the takecare chart with
// the demo profile.
func addonRBAC(t *testing.T, name string) []*unstructured.Unstructured {
	t.Helper()
	helm := os.Getenv("HELM")
	if helm == "" {
		t.Fatal("HELM is not set: run `make test`, which downloads the pinned Helm and the charts' dependencies")
	}
	chart := filepath.Join(RepoRoot(), "charts", "takecare")
	cmd := exec.Command(helm, "template", "takecare", chart, "-n", OperatorNamespace, //nolint:gosec // the test's own binary and arguments
		"-f", filepath.Join(chart, "values-demo.yaml"), "--show-only", "templates/addons/"+name+".yaml")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("helm template takecare (add-on %s): %v\n%s", name, err, stderr.String())
	}
	var objs []*unstructured.Unstructured
	dec := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(out), 4096)
	for {
		u := &unstructured.Unstructured{}
		err := dec.Decode(&u.Object)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("decode the rendered add-on: %v", err)
		}
		// Role, RoleBinding, ClusterRole, ClusterRoleBinding.
		if k := u.GetKind(); strings.HasSuffix(k, "Role") || strings.HasSuffix(k, "RoleBinding") {
			objs = append(objs, u)
		}
	}
	if len(objs) == 0 {
		t.Fatalf("the add-on %s renders no RBAC in the demo profile", name)
	}
	return objs
}
