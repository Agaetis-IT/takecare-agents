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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/testenv"
)

// The rights spec 13 §6 documents for review, word for word: get and
// list on the Intendants and surveys, get on ConfigMaps in the operator's
// namespace.
var reviewRights = []rbacv1.PolicyRule{
	{APIGroups: []string{v1alpha1.GroupVersion.Group}, Resources: []string{"takecareintendants", "takecaresurveys"}, Verbs: []string{verbGet, "list"}},
	{APIGroups: []string{""}, Resources: []string{"configmaps"}, Verbs: []string{verbGet}},
}

// Spec 13 §6 and §10: review runs as a user holding only the rights of §6,
// which proves they are enough and that no write is attempted; without the
// ConfigMap right it works on the status's proposals and says so; a user
// without rights gets exit code 2.
func TestReviewAsReader(t *testing.T) {
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
	const kindsOnly = "takecare-kinds-only"
	mustCreate(&corev1.Namespace{Name: testNS})
	mustCreate(&rbacv1.Role{Name: readerRole, Namespace: testNS, Rules: reviewRights})
	mustCreate(&rbacv1.Role{Name: kindsOnly, Namespace: testNS, Rules: reviewRights[:1]})
	for user, role := range map[string]string{readerUser: readerRole, kindsOnly: kindsOnly} {
		mustCreate(&rbacv1.RoleBinding{
			Name: role, Namespace: testNS,
			RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: role},
			Subjects: []rbacv1.Subject{{APIGroup: rbacv1.GroupName, Kind: rbacv1.UserKind, Name: user}},
		})
	}
	scope := v1alpha1.Scope{Namespaces: []v1alpha1.Namespace{nsApps}}
	mustCreate(&v1alpha1.TakeCareIntendant{Name: intendantName, Namespace: testNS,
		Spec: v1alpha1.TakeCareIntendantSpec{RoleCommonSpec: v1alpha1.RoleCommonSpec{Scope: scope}, Connectors: v1alpha1.IntendantConnectors{Map: []string{"map"}}}})
	objs := surveyObjects(v1alpha1.RunCompleted, true)
	survey, cm := objs[0].(*v1alpha1.TakeCareSurvey), objs[2].(*corev1.ConfigMap)
	status := survey.Status
	survey.ObjectMeta = metav1.ObjectMeta{Name: surveyName, Namespace: testNS}
	survey.Status = v1alpha1.TakeCareSurveyStatus{}
	mustCreate(survey)
	survey.Status = status
	survey.Status.StartedAt = &metav1.Time{Time: time.Now()}
	if err := admin.Status().Update(ctx, survey); err != nil {
		t.Fatalf("survey status: %v", err)
	}
	mustCreate(cm)

	run := func(user string) (int, Review, string) {
		t.Helper()
		var res Review
		code, out, stderr := 0, "", ""
		// RBAC reaches the authorizer's cache shortly after the binding.
		_ = wait.PollUntilContextTimeout(ctx, 200*time.Millisecond, 30*time.Second, true, func(ctx context.Context) (bool, error) {
			code, out, stderr = runReviewAs(ctx, t, kubeconfigAs(t, cfg, user))
			forbidden := strings.Contains(stderr, "forbidden")
			return user == "nobody" || code != ExitError && (user != readerUser || !forbidden), nil
		})
		if code == ExitOK {
			if err := json.Unmarshal([]byte(out), &res); err != nil {
				t.Fatalf("review as %s: %v: %s", user, err, out)
			}
		}
		return code, res, stderr
	}

	code, res, stderr := run(readerUser)
	if code != ExitOK || len(res.Accepted) != 3 || res.NotRead != 0 {
		t.Errorf("review as %s: exit %d, accepted %v, notRead %d; stderr %q", readerUser, code, res.Accepted, res.NotRead, stderr)
	}
	code, res, stderr = run(kindsOnly)
	if code != ExitOK || len(res.Accepted) != 1 || res.NotRead != 2 || !strings.Contains(stderr, "could not read the ConfigMap") {
		t.Errorf("review as %s: exit %d, accepted %v, notRead %d; stderr %q", kindsOnly, code, res.Accepted, res.NotRead, stderr)
	}
	// The check bites: without the rights, review cannot run.
	code, _, stderr = run("nobody")
	if code != ExitError || !strings.Contains(stderr, "forbidden") {
		t.Errorf("review as a user without rights: exit %d, stderr %q; want 2 and forbidden", code, stderr)
	}
}

func runReviewAs(ctx context.Context, t *testing.T, kubeconfig string) (code int, stdout, stderr string) {
	dir, state := t.TempDir(), t.TempDir()
	writeTree(t, dir, fixtureMap)
	var out, errOut bytes.Buffer
	a := &app{stdin: strings.NewReader(""), stdout: &out, stderr: &errOut, now: time.Now}
	code = a.run(ctx, []string{cmdReview, flagKubecfg, kubeconfig, flagAccept, acceptAll, flagNoCommit,
		flagMapDir, dir, flagStateDir, state, flagOutput, outputJSON})
	return code, out.String(), errOut.String()
}
