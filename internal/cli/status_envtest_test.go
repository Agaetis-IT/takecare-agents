// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/testenv"
)

// The rights spec 13 §6 documents for status, word for word.
var statusRights = []rbacv1.PolicyRule{{
	APIGroups: []string{v1alpha1.GroupVersion.Group},
	Resources: []string{"takecareplatforms", "takecaredocs", "takecareintendants", "takecarerangers",
		"takecareconnectors", "takecaresurveys", "takecareinvestigations"},
	Verbs: []string{verbGet, "list"},
}}

const (
	readerUser = "takecare-reader"
	readerRole = "takecare-reader"
)

// Spec 13 §6 and §10: status runs as a user holding only get and list on
// the TakeCare kinds, which proves those rights are enough and that no
// write is attempted; a user without them gets exit code 2.
func TestStatusAsReader(t *testing.T) {
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
	mustCreate(&rbacv1.ClusterRole{Name: readerRole, Rules: statusRights})
	mustCreate(&rbacv1.ClusterRoleBinding{
		Name:     readerRole,
		RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: readerRole},
		Subjects: []rbacv1.Subject{{APIGroup: rbacv1.GroupName, Kind: rbacv1.UserKind, Name: readerUser}},
	})

	ready := func() []metav1.Condition {
		return []metav1.Condition{{Type: v1alpha1.ConditionReady, Status: metav1.ConditionTrue, Reason: "Ready", LastTransitionTime: metav1.Now()}}
	}
	scope := v1alpha1.Scope{Namespaces: []v1alpha1.Namespace{nsApps}}
	platform := &v1alpha1.TakeCarePlatform{Name: "main", Spec: v1alpha1.TakeCarePlatformSpec{Models: []v1alpha1.ModelSpec{{
		Name: "default", BaseURL: "https://models.example/v1", Model: "m", APIKeySecretRef: v1alpha1.SecretKeyRef{Name: "model", Key: "apiKey"},
	}}}}
	doc := &v1alpha1.TakeCareDoc{Name: "doc", Namespace: testNS,
		Spec: v1alpha1.TakeCareDocSpec{RoleCommonSpec: v1alpha1.RoleCommonSpec{Scope: scope}}}
	intendant := &v1alpha1.TakeCareIntendant{Name: intendantName, Namespace: testNS,
		Spec: v1alpha1.TakeCareIntendantSpec{RoleCommonSpec: v1alpha1.RoleCommonSpec{Scope: scope}, Connectors: v1alpha1.IntendantConnectors{Map: []string{"map"}}}}
	ranger := &v1alpha1.TakeCareRanger{Name: "main"}
	connector := &v1alpha1.TakeCareConnector{Name: "kubernetes", Namespace: testNS,
		Spec: v1alpha1.TakeCareConnectorSpec{Types: []v1alpha1.ConnectorType{v1alpha1.ConnectorCluster}, MCP: v1alpha1.MCPSpec{Addon: "kubernetes"},
			Kubernetes: &v1alpha1.KubernetesSpec{Namespaces: []v1alpha1.Namespace{nsApps}}}}
	survey := &v1alpha1.TakeCareSurvey{Name: "intendant-1", Namespace: testNS,
		Spec: v1alpha1.TakeCareSurveySpec{IntendantRef: v1alpha1.RoleRef{Kind: v1alpha1.KindTakeCareIntendant, Name: intendantName}, Trigger: v1alpha1.TriggerOnDemand}}
	investigation := &v1alpha1.TakeCareInvestigation{Name: "doc-1", Namespace: testNS,
		Spec: v1alpha1.TakeCareInvestigationSpec{RoleRef: v1alpha1.RoleRef{Kind: "TakeCareDoc", Name: "doc"},
			Alert: v1alpha1.AlertRecord{Name: alertName}, Level: v1alpha1.LevelApprentice}}
	for _, obj := range []client.Object{platform, doc, intendant, ranger, connector, survey, investigation} {
		mustCreate(obj)
	}
	platform.Status.Conditions = ready()
	doc.Status.Conditions, doc.Status.Level = ready(), v1alpha1.LevelApprentice
	intendant.Status.Conditions, intendant.Status.Level = ready(), v1alpha1.LevelApprentice
	ranger.Status.Conditions = ready()
	connector.Status.Conditions = ready()
	connector.Status.Tools = []v1alpha1.ToolStatus{{Name: "list_pods", Class: v1alpha1.ToolRead, Source: v1alpha1.ToolSourceAnnotation}}
	survey.Status.Phase = v1alpha1.RunCompleted
	investigation.Status.Phase = v1alpha1.RunFailed
	investigation.Status.Reason = reasonTimedOut
	for _, obj := range []client.Object{platform, doc, intendant, ranger, connector, survey, investigation} {
		if err := admin.Status().Update(ctx, obj); err != nil {
			t.Fatalf("status of %T %s: %v", obj, obj.GetName(), err)
		}
	}

	reader := kubeconfigAs(t, cfg, readerUser)
	// RBAC reaches the authorizer's cache shortly after the binding.
	code, out, stderr := awaitStatus(ctx, t, reader)
	if code != ExitOK {
		t.Fatalf("status as %s: exit %d, want 0; stderr %q", readerUser, code, stderr)
	}
	var s Status
	if err := json.Unmarshal([]byte(out), &s); err != nil {
		t.Fatalf("status --output json: %v\n%s", err, out)
	}
	roles := make([]string, 0, len(s.Roles))
	for _, r := range s.Roles {
		roles = append(roles, r.Kind+"/"+r.Name)
	}
	if diff := cmp.Diff([]string{"TakeCareDoc/doc", "TakeCareIntendant/intendant", "TakeCareRanger/main"}, roles); diff != "" {
		t.Errorf("roles (-want +got):\n%s", diff)
	}
	if len(s.Platforms) != 1 || len(s.Connectors) != 1 || s.Connectors[0].Tools.Read != 1 {
		t.Errorf("platforms %+v, connectors %+v", s.Platforms, s.Connectors)
	}
	if len(s.Surveys) != 1 || len(s.Investigations) != 1 || s.Investigations[0].Reason != reasonTimedOut {
		t.Errorf("surveys %+v, investigations %+v", s.Surveys, s.Investigations)
	}

	// A connector that turns not Ready: exit code 1, naming it.
	connector.Status.Conditions[0].Status = metav1.ConditionFalse
	connector.Status.Conditions[0].Reason = "Unreachable"
	if err := admin.Status().Update(ctx, connector); err != nil {
		t.Fatal(err)
	}
	code, out, stderr = runStatus(ctx, reader)
	if code != ExitFinding {
		t.Fatalf("status with a connector not Ready: exit %d, want 1; stderr %q", code, stderr)
	}
	if err := json.Unmarshal([]byte(out), &s); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"TakeCareConnector/kubernetes"}, s.NotReady); diff != "" {
		t.Errorf("notReady (-want +got):\n%s", diff)
	}

	// The check bites: without the rights, status cannot run.
	code, _, stderr = runStatus(ctx, kubeconfigAs(t, cfg, "nobody"))
	if code != ExitError || !strings.Contains(stderr, "forbidden") {
		t.Errorf("status as a user without rights: exit %d, stderr %q; want 2 and forbidden", code, stderr)
	}
}

// kubeconfigAs writes a kubeconfig on the envtest server that impersonates
// user, and returns its path.
func kubeconfigAs(t *testing.T, cfg *rest.Config, user string) string {
	t.Helper()
	kc := clientcmdapi.NewConfig()
	kc.Clusters["envtest"] = &clientcmdapi.Cluster{Server: cfg.Host, CertificateAuthorityData: cfg.CAData}
	kc.AuthInfos["admin"] = &clientcmdapi.AuthInfo{ClientCertificateData: cfg.CertData, ClientKeyData: cfg.KeyData, Impersonate: user}
	kc.Contexts["envtest"] = &clientcmdapi.Context{Cluster: "envtest", AuthInfo: "admin"}
	kc.CurrentContext = "envtest"
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := clientcmd.WriteToFile(*kc, path); err != nil {
		t.Fatal(err)
	}
	return path
}

func runStatus(ctx context.Context, kubeconfig string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	a := &app{stdout: &out, stderr: &errOut, now: time.Now}
	code = a.run(ctx, []string{"status", flagKubecfg, kubeconfig, flagOutput, outputJSON})
	return code, out.String(), errOut.String()
}

// awaitStatus runs status until it stops failing on a forbidden list, for
// up to 30 s.
func awaitStatus(ctx context.Context, t *testing.T, kubeconfig string) (code int, stdout, stderr string) {
	t.Helper()
	_ = wait.PollUntilContextTimeout(ctx, 200*time.Millisecond, 30*time.Second, true, func(ctx context.Context) (bool, error) {
		code, stdout, stderr = runStatus(ctx, kubeconfig)
		return code != ExitError || !strings.Contains(stderr, "forbidden"), nil
	})
	return code, stdout, stderr
}
