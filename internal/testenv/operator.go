// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package testenv

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// The namespaces and the ServiceAccount the chart's RBAC is rendered for:
// the release namespace and the engine's, as the Roles name them.
const (
	OperatorNamespace = "takecare-system"
	EngineNamespace   = "kagent"
	operatorUser      = "system:serviceaccount:" + OperatorNamespace + ":takecare-operator"
)

// operatorGroups are the groups a ServiceAccount token carries.
var operatorGroups = []string{"system:serviceaccounts", "system:serviceaccounts:" + OperatorNamespace, "system:authenticated"}

// Env is an API server with the product's CRDs and two identities: Admin
// for the tests' fixtures and assertions, Operator for the managers under
// test, which the API server authorizes as the operator's ServiceAccount
// bound to the RBAC the takecare chart renders. envtest as admin once hid
// a List the operator's Roles refuse (0.3.0); under Operator, a request
// the operator could not make on a cluster fails the test.
type Env struct {
	Admin, Operator *rest.Config
}

// StartOperator is Start, with the operator's identity. The operator and
// engine namespaces exist when it returns, and every namespace the rendered
// RBAC names.
func StartOperator(t *testing.T) Env {
	t.Helper()
	return startOperator(t, false, failOnRefusals(t))
}

// StartDemoOperator is StartOperator with the demo's CRDs and the demo's
// operator rights (templates/demo/operator-roles.yaml).
func StartDemoOperator(t *testing.T) Env {
	t.Helper()
	return startOperator(t, true, failOnRefusals(t))
}

// failOnRefusals fails the test, at its end, on any request refused to the
// operator.
func failOnRefusals(t *testing.T) func([]string) {
	return func(got []string) {
		if len(got) > 0 {
			t.Errorf("the API server refused the operator %d request(s), which its Roles would refuse on a cluster: %s", len(got), strings.Join(got, "; "))
		}
	}
}

// startOperator hands the requests refused to the operator to onRefusals
// as the test ends.
func startOperator(t *testing.T, demo bool, onRefusals func([]string)) Env {
	t.Helper()
	env, admin := start(t, demo)
	c, err := client.New(admin, client.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	rbac := chartRBAC(t, demo)
	createNamespaces(t, c, rbac)
	for _, obj := range rbac {
		if err := c.Create(ctx, obj); err != nil {
			t.Fatalf("create %s %s: %v", obj.GetKind(), obj.GetName(), err)
		}
	}
	user, err := env.AddUser(envtest.User{
		Name:   operatorUser,
		Groups: operatorGroups,
	}, &rest.Config{QPS: admin.QPS, Burst: admin.Burst})
	if err != nil {
		t.Fatalf("add the operator's user: %v", err)
	}
	waitForRights(t, c, rbac)
	operator := user.Config()
	refusals := &refusals{}
	operator.Wrap(func(rt http.RoundTripper) http.RoundTripper { return refusals.watch(rt) })
	t.Cleanup(func() { onRefusals(refusals.list()) })
	return Env{Admin: admin, Operator: operator}
}

// createNamespaces creates the operator and engine namespaces, and every
// namespace the rendered RBAC names.
func createNamespaces(t *testing.T, c client.Client, rbac []*unstructured.Unstructured) {
	t.Helper()
	namespaces := []string{OperatorNamespace, EngineNamespace}
	for _, obj := range rbac {
		if ns := obj.GetNamespace(); ns != "" && !slices.Contains(namespaces, ns) {
			namespaces = append(namespaces, ns)
		}
	}
	for _, ns := range namespaces {
		if err := c.Create(context.Background(), &corev1.Namespace{Name: ns}); err != nil {
			t.Fatalf("create namespace %s: %v", ns, err)
		}
	}
}

// rightsTimeout bounds the wait for the authorizer to see the operator's
// RBAC: seconds under load, never more.
const rightsTimeout = 30 * time.Second

// reviewTimeout bounds one SubjectAccessReview.
const reviewTimeout = 10 * time.Second

// waitForRights blocks until the API server's authorizer grants the
// operator one rule of each binding the chart renders. The RBAC authorizer
// reads Roles and bindings from informer caches (kube-apiserver v1.37.0
// pkg/kubeapiserver/authorizer/config.go, rbac.New over the listers), so a
// binding just created may not be seen yet: a manager started at once could
// be refused a List its Roles grant. That this lag caused the refusals of
// the intendant package's one failed run was not reproduced; the wait
// rules the lag out all the same. A rule of a binding is granted
// only once both the binding and its role are in the caches, and the
// role's other rules come with it.
func waitForRights(t *testing.T, c client.Client, rbac []*unstructured.Unstructured) {
	t.Helper()
	if err := awaitRights(context.Background(), c, rightChecks(t, rbac), rightsTimeout); err != nil {
		t.Fatal(err)
	}
}

// awaitRights asks the authorizer, through SubjectAccessReviews as the
// operator, until it grants every check or the timeout passes. It grants
// nothing.
func awaitRights(ctx context.Context, c client.Client, checks []rightCheck, timeout time.Duration) error {
	var pending []string
	// Each pass runs whole, on the caller's context: cut at the deadline,
	// its reviews would report the deadline instead of the authorizer.
	err := wait.PollUntilContextTimeout(ctx, 50*time.Millisecond, timeout, true, func(context.Context) (bool, error) {
		pending = pending[:0]
		for _, ch := range checks {
			sar := &authorizationv1.SubjectAccessReview{Spec: authorizationv1.SubjectAccessReviewSpec{
				User:                  operatorUser,
				Groups:                operatorGroups,
				ResourceAttributes:    ch.resource,
				NonResourceAttributes: ch.nonResource,
			}}
			// A failed review is retried: the client's first request
			// discovers the API, which a loaded server may answer late.
			rctx, cancel := context.WithTimeout(ctx, reviewTimeout)
			err := c.Create(rctx, sar)
			cancel()
			if err != nil {
				pending = append(pending, fmt.Sprintf("%s (review failed: %v)", ch.name, err))
				continue
			}
			if !sar.Status.Allowed {
				pending = append(pending, ch.name)
			}
		}
		return len(pending) == 0, nil
	})
	if err != nil {
		return fmt.Errorf("the authorizer did not grant the operator its Roles within %s (%w); not granted: %s", timeout, err, strings.Join(pending, "; "))
	}
	return nil
}

// rightCheck is one access the operator's RBAC grants, named for errors.
type rightCheck struct {
	name        string
	resource    *authorizationv1.ResourceAttributes
	nonResource *authorizationv1.NonResourceAttributes
}

// rightChecks takes, for each binding, the first rule of the role it binds,
// in the binding's namespace.
func rightChecks(t *testing.T, rbac []*unstructured.Unstructured) []rightCheck {
	t.Helper()
	roles := map[string]*unstructured.Unstructured{}
	for _, o := range rbac {
		if k := o.GetKind(); k == "Role" || k == "ClusterRole" {
			roles[k+"/"+o.GetNamespace()+"/"+o.GetName()] = o
		}
	}
	var checks []rightCheck
	for _, b := range rbac {
		if k := b.GetKind(); k != "RoleBinding" && k != "ClusterRoleBinding" {
			continue
		}
		kind, _, _ := unstructured.NestedString(b.Object, "roleRef", "kind")
		name, _, _ := unstructured.NestedString(b.Object, "roleRef", "name")
		ns := b.GetNamespace()
		key := kind + "/" + ns + "/" + name
		if kind == "ClusterRole" {
			key = kind + "//" + name
		}
		role, ok := roles[key]
		if !ok {
			t.Fatalf("%s %s/%s binds %s, which the rendered RBAC lacks", b.GetKind(), ns, b.GetName(), key)
		}
		rules, _, _ := unstructured.NestedSlice(role.Object, "rules")
		if len(rules) == 0 {
			t.Fatalf("%s binds %s, which has no rule", b.GetName(), key)
		}
		rule, _ := rules[0].(map[string]any)
		first := func(field string) string {
			s, _, _ := unstructured.NestedStringSlice(rule, field)
			if len(s) == 0 {
				return ""
			}
			return s[0]
		}
		ch := rightCheck{name: fmt.Sprintf("%s %s/%s: %s", b.GetKind(), ns, b.GetName(), first("verbs"))}
		if path := first("nonResourceURLs"); path != "" {
			ch.name += " " + path
			ch.nonResource = &authorizationv1.NonResourceAttributes{Path: path, Verb: first("verbs")}
		} else {
			resource, sub, _ := strings.Cut(first("resources"), "/")
			ch.name += " " + first("resources")
			ch.resource = &authorizationv1.ResourceAttributes{
				Namespace: ns, Verb: first("verbs"), Group: first("apiGroups"),
				Resource: resource, Subresource: sub, Name: first("resourceNames"),
			}
		}
		checks = append(checks, ch)
	}
	return checks
}

// refusals records the requests answered 403: a controller that retries a
// refused List would otherwise pass, silent, as the operator restarts on a
// cluster.
type refusals struct {
	mu   sync.Mutex
	seen []string
}

func (r *refusals) watch(next http.RoundTripper) http.RoundTripper {
	return roundTripper(func(req *http.Request) (*http.Response, error) {
		resp, err := next.RoundTrip(req)
		if err == nil && resp.StatusCode == http.StatusForbidden {
			r.mu.Lock()
			r.seen = append(r.seen, req.Method+" "+req.URL.Path)
			r.mu.Unlock()
		}
		return resp, err
	})
}

func (r *refusals) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.seen)
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// chartRBAC renders the operator's Roles, ClusterRoles and their bindings
// from the takecare chart, as an installation gets them. The rules do not
// depend on the profile; the demo's values only let the chart render.
func chartRBAC(t *testing.T, demo bool) []*unstructured.Unstructured {
	t.Helper()
	args := []string{"template", "takecare", filepath.Join(RepoRoot(), "charts", "takecare"), "-n", OperatorNamespace,
		"-f", filepath.Join(RepoRoot(), "charts", "takecare", "values-demo.yaml"),
		"--show-only", "templates/operator/roles.yaml", "--show-only", "templates/operator/rbac.yaml"}
	if demo {
		args = append(args, "--show-only", "templates/demo/operator-roles.yaml")
	}
	helm := os.Getenv("HELM")
	if helm == "" {
		t.Fatal("HELM is not set: run `make test`, which downloads the pinned Helm and the charts' dependencies")
	}
	cmd := exec.Command(helm, args...) //nolint:gosec // the test's own binary and arguments
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("helm template takecare (operator RBAC): %v\n%s", err, stderr.String())
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
			t.Fatalf("decode the rendered RBAC: %v", err)
		}
		if len(u.Object) > 0 {
			objs = append(objs, u)
		}
	}
	// The chart grants through bindings to these: a render without them
	// would leave the operator with no right and the tests meaningless.
	kinds := map[string]int{}
	for _, o := range objs {
		kinds[o.GetKind()]++
	}
	if kinds["Role"] == 0 || kinds["RoleBinding"] == 0 || kinds["ClusterRole"] == 0 || kinds["ClusterRoleBinding"] == 0 {
		t.Fatalf("the rendered operator RBAC lacks a kind: %v", kinds)
	}
	return objs
}
