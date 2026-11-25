// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/testenv"
)

const (
	resIntendants = "takecareintendants"
	resSurveys    = "takecaresurveys"
	resConnectors = "takecareconnectors"
	verbList      = "list"
)

// The rights spec 13 §6 documents for discover, word for word.
var discoverRights = []rbacv1.PolicyRule{{
	APIGroups: []string{v1alpha1.GroupVersion.Group},
	Resources: []string{resIntendants, resSurveys, resConnectors},
	Verbs:     []string{verbGet, verbList},
}}

// Spec 13 §3.3, §6 and §10: discover runs as a user holding only the
// rights of §6, which proves they are enough and that no write is
// attempted; it shows the newest survey, says it predates the served Map,
// and creates nothing; a user without rights gets exit code 2.
func TestDiscoverAsReader(t *testing.T) {
	cfg := testenv.Start(t)
	ctx := t.Context()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{v1alpha1.AddToScheme, corev1.AddToScheme, rbacv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	admin, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	mustCreate := func(obj client.Object) {
		t.Helper()
		if err := admin.Create(ctx, obj); err != nil {
			t.Fatalf("create %T %s: %v", obj, obj.GetName(), err)
		}
	}
	mustCreate(&corev1.Namespace{Name: testNS})
	mustCreate(&rbacv1.Role{Name: readerRole, Namespace: testNS, Rules: discoverRights})
	mustCreate(&rbacv1.RoleBinding{
		Name: readerRole, Namespace: testNS,
		RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: readerRole},
		Subjects: []rbacv1.Subject{{APIGroup: rbacv1.GroupName, Kind: rbacv1.UserKind, Name: readerUser}},
	})
	scope := v1alpha1.Scope{Namespaces: []v1alpha1.Namespace{nsApps}}
	mustCreate(&v1alpha1.TakeCareIntendant{Name: intendantName, Namespace: testNS,
		Spec: v1alpha1.TakeCareIntendantSpec{RoleCommonSpec: v1alpha1.RoleCommonSpec{Scope: scope},
			Connectors: v1alpha1.IntendantConnectors{Map: []string{mapConnector}}}})
	connector := &v1alpha1.TakeCareConnector{Name: mapConnector, Namespace: testNS,
		Spec: v1alpha1.TakeCareConnectorSpec{Types: []v1alpha1.ConnectorType{v1alpha1.ConnectorMap}, MCP: v1alpha1.MCPSpec{Addon: string(v1alpha1.ConnectorMap)}}}
	mustCreate(connector)
	connector.Status.MapRevision = servedRev
	if err := admin.Status().Update(ctx, connector); err != nil {
		t.Fatalf("connector status: %v", err)
	}
	survey := &v1alpha1.TakeCareSurvey{Name: surveyName, Namespace: testNS,
		Spec: v1alpha1.TakeCareSurveySpec{IntendantRef: v1alpha1.RoleRef{Kind: v1alpha1.KindTakeCareIntendant, Name: intendantName}}}
	mustCreate(survey)
	survey.Status.Phase = v1alpha1.RunCompleted
	survey.Status.MapRevision = surveyRev
	if err := admin.Status().Update(ctx, survey); err != nil {
		t.Fatalf("survey status: %v", err)
	}

	run := func(user string) (int, string, string) {
		t.Helper()
		code, out, stderr := 0, "", ""
		// RBAC reaches the authorizer's cache shortly after the binding.
		_ = wait.PollUntilContextTimeout(ctx, 200*time.Millisecond, 30*time.Second, true, func(ctx context.Context) (bool, error) {
			var o, e bytes.Buffer
			a := &app{stdout: &o, stderr: &e, now: time.Now}
			code = a.run(ctx, []string{"discover", flagKubecfg, kubeconfigAs(t, cfg, user), flagOutput, outputJSON})
			out, stderr = o.String(), e.String()
			return user == "nobody" || code != ExitError || !strings.Contains(stderr, "forbidden"), nil
		})
		return code, out, stderr
	}

	code, out, stderr := run(readerUser)
	var d Discover
	if code != ExitOK {
		t.Fatalf("discover as %s: exit %d, want 0; stderr %q", readerUser, code, stderr)
	}
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		t.Fatalf("discover --output json: %v\n%s", err, out)
	}
	if d.Survey == nil || d.Survey.Name != surveyName || !d.Survey.PredatesServedMap || d.ServedRevision != servedRev ||
		!strings.Contains(d.Command, "name: "+intendantName+" }") {
		t.Errorf("discover = %+v, survey %+v", d, d.Survey)
	}
	// It creates nothing: the one survey is still the only one.
	var ss v1alpha1.TakeCareSurveyList
	if err := admin.List(ctx, &ss, client.InNamespace(testNS)); err != nil || len(ss.Items) != 1 {
		t.Errorf("surveys after discover: %d, err %v; want 1", len(ss.Items), err)
	}
	// The check bites: without the rights, discover cannot run.
	code, _, stderr = run("nobody")
	if code != ExitError || !strings.Contains(stderr, "forbidden") {
		t.Errorf("discover as a user without rights: exit %d, stderr %q; want 2 and forbidden", code, stderr)
	}
}
