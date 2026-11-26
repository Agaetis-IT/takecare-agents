// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package v1alpha1_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

const (
	connCluster      = "kubernetes"
	connCode         = "code"
	connPropose      = "code-propose"
	repoShop         = "acme/shop-deploy"
	repoPlatform     = "acme/platform/charts"
	githubAPI        = "https://api.github.com"
	wantOneCluster   = "the Mécano needs at least one cluster connector"
	wantOneCode      = "the Mécano needs at least one code connector"
	wantRepoPropose  = "each maintenance repository names the propose connector"
	wantNoneLower    = "no scheduled maintenance is written 'none', in lower case"
	compBilling      = "service/billing-api"
	branchOfRecord   = "takecare/mecano/maintenance-0123456789"
	wantPropose      = "spec.propose is only valid for a connector of type code served by an add-on"
	connReleases     = "releases"
	wantRepositories = "repositories"
	wantRepository   = "repository"
	wantComponents   = "components"
)

func mecanoFixture() *v1alpha1.TakeCareMecano {
	return &v1alpha1.TakeCareMecano{
		Name: uniqueName("mecano"), Namespace: testNamespace,
		Spec: v1alpha1.TakeCareMecanoSpec{
			Level:        v1alpha1.LevelPractitioner,
			Scope:        v1alpha1.Scope{Namespaces: []v1alpha1.Namespace{nsDemoApps}},
			ContentTools: []string{"file_get"},
			Connectors: v1alpha1.MecanoConnectors{
				Cluster:   []string{connCluster},
				Code:      []string{connCode},
				Propose:   []string{connPropose},
				Copy:      []string{"staging"},
				Releases:  []string{connReleases},
				Knowledge: []string{connRunbooks},
				Changes:   []string{"deployments"},
				History:   []string{connHistory},
				Map:       []string{connMap},
				Report:    connReport,
			},
			Report:   v1alpha1.ReportDestinations{Apprentice: destReview, Practitioner: "platform-team"},
			Schedule: "0 4 * * 2",
			Maintenance: v1alpha1.MecanoMaintenanceSettings{
				Repositories: []v1alpha1.MaintenanceRepository{
					{Connector: connPropose, Repository: repoShop},
					{Connector: connPropose, Repository: repoPlatform},
				},
			},
		},
	}
}

func repositories(n int) []v1alpha1.MaintenanceRepository {
	out := make([]v1alpha1.MaintenanceRepository, n)
	for i := range out {
		out[i] = v1alpha1.MaintenanceRepository{Connector: connPropose, Repository: fmt.Sprintf("acme/r%d", i)}
	}
	return out
}

func TestMecanoValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*v1alpha1.TakeCareMecano)
		want   string
	}{
		{"complete Mécano", func(*v1alpha1.TakeCareMecano) {}, ""},
		{"minimal Mécano", func(m *v1alpha1.TakeCareMecano) {
			m.Spec = v1alpha1.TakeCareMecanoSpec{
				Scope:      m.Spec.Scope,
				Connectors: v1alpha1.MecanoConnectors{Cluster: []string{connCluster}, Code: []string{connCode}},
			}
		}, ""},
		{"scope by entity", func(m *v1alpha1.TakeCareMecano) {
			m.Spec.Scope = v1alpha1.Scope{Entities: []v1alpha1.EntityKey{entityBilling}}
		}, ""},
		{"no cluster connector", func(m *v1alpha1.TakeCareMecano) { m.Spec.Connectors.Cluster = nil }, wantOneCluster},
		{"no code connector", func(m *v1alpha1.TakeCareMecano) { m.Spec.Connectors.Code = nil }, wantOneCode},
		{"16 cluster connectors", func(m *v1alpha1.TakeCareMecano) { m.Spec.Connectors.Cluster = names(16) }, ""},
		{"cluster slot past 16", func(m *v1alpha1.TakeCareMecano) { m.Spec.Connectors.Cluster = names(17) }, "connectors.cluster"},
		{"17 code connectors", func(m *v1alpha1.TakeCareMecano) { m.Spec.Connectors.Code = names(17) }, connCode},
		{"two propose connectors", func(m *v1alpha1.TakeCareMecano) { m.Spec.Connectors.Propose = []string{connPropose, "second-propose"} }, "propose"},
		{"two copy connectors", func(m *v1alpha1.TakeCareMecano) { m.Spec.Connectors.Copy = []string{"a", "b"} }, "copy"},
		{"4 releases connectors", func(m *v1alpha1.TakeCareMecano) { m.Spec.Connectors.Releases = names(4) }, ""},
		{"5 releases connectors", func(m *v1alpha1.TakeCareMecano) { m.Spec.Connectors.Releases = names(5) }, "releases"},
		{case17Knowledge, func(m *v1alpha1.TakeCareMecano) { m.Spec.Connectors.Knowledge = names(17) }, wantKnowledge},
		{"changes slot past 16", func(m *v1alpha1.TakeCareMecano) { m.Spec.Connectors.Changes = names(17) }, "connectors.changes"},
		{"two history connectors", func(m *v1alpha1.TakeCareMecano) { m.Spec.Connectors.History = []string{"a", "b"} }, "connectors.history"},
		{"map slot past 1", func(m *v1alpha1.TakeCareMecano) { m.Spec.Connectors.Map = []string{connMap, "second-map"} }, connMap},
		{"code name past 253", func(m *v1alpha1.TakeCareMecano) { m.Spec.Connectors.Code = []string{strings.Repeat("c", 254)} }, connCode},
		{"report name past 253", func(m *v1alpha1.TakeCareMecano) { m.Spec.Connectors.Report = strings.Repeat("r", 254) }, connReport},
		{caseUnknownLevel, func(m *v1alpha1.TakeCareMecano) { m.Spec.Level = badLevel }, wantLevel},
		{caseEmptyScope, func(m *v1alpha1.TakeCareMecano) { m.Spec.Scope.Namespaces = nil }, wantSelector},
		{"schedule with CRON_TZ=", func(m *v1alpha1.TakeCareMecano) { m.Spec.Schedule = "CRON_TZ=Europe/Paris 0 5 * * 1" }, v1alpha1.MessageScheduleUTC},
		{"no scheduled maintenance", func(m *v1alpha1.TakeCareMecano) { m.Spec.Schedule = v1alpha1.ScheduleNone }, ""},
		{"none in upper case", func(m *v1alpha1.TakeCareMecano) { m.Spec.Schedule = "NONE" }, wantNoneLower},
		{"schedule past 100", func(m *v1alpha1.TakeCareMecano) { m.Spec.Schedule = strings.Repeat("*", 101) }, "spec.schedule"},
		{caseNameAtBound, func(m *v1alpha1.TakeCareMecano) { m.Name = padded(m.Name, v1alpha1.MaxMecanoName) }, ""},
		{caseNamePastBound, func(m *v1alpha1.TakeCareMecano) { m.Name = padded(m.Name, v1alpha1.MaxMecanoName+1) }, v1alpha1.MessageMecanoName},
		{"maintenance settings", func(m *v1alpha1.TakeCareMecano) {
			m.Spec.Maintenance.Timeout, m.Spec.Maintenance.CertificateWarning = duration(30*time.Minute), duration(14*24*time.Hour)
		}, ""},
		{"zero timeout", func(m *v1alpha1.TakeCareMecano) { m.Spec.Maintenance.Timeout = duration(0) }, "maintenance.timeout must be positive"},
		{"zero certificate warning", func(m *v1alpha1.TakeCareMecano) {
			m.Spec.Maintenance.CertificateWarning = duration(0)
		}, "maintenance.certificateWarning must be positive"},
		{"16 repositories", func(m *v1alpha1.TakeCareMecano) { m.Spec.Maintenance.Repositories = repositories(16) }, ""},
		{"17 repositories", func(m *v1alpha1.TakeCareMecano) { m.Spec.Maintenance.Repositories = repositories(17) }, wantRepositories},
		{"repository twice", func(m *v1alpha1.TakeCareMecano) {
			m.Spec.Maintenance.Repositories[1].Repository = repoShop
		}, wantDuplicate},
		{"repository of another connector", func(m *v1alpha1.TakeCareMecano) {
			m.Spec.Maintenance.Repositories[0].Connector = connCode
		}, wantRepoPropose},
		{"repositories without a propose connector", func(m *v1alpha1.TakeCareMecano) { m.Spec.Connectors.Propose = nil }, wantRepoPropose},
		{"a propose connector and no repository", func(m *v1alpha1.TakeCareMecano) { m.Spec.Maintenance.Repositories = nil }, ""},
		{"empty repository", func(m *v1alpha1.TakeCareMecano) { m.Spec.Maintenance.Repositories[0].Repository = "" }, wantRepository},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := mecanoFixture()
			tt.mutate(m)
			checkError(t, create(t, m), tt.want)
		})
	}
}

func TestMecanoDefaults(t *testing.T) {
	m := mecanoFixture()
	m.Spec = v1alpha1.TakeCareMecanoSpec{
		Scope:      m.Spec.Scope,
		Connectors: v1alpha1.MecanoConnectors{Cluster: []string{connCluster}, Code: []string{connCode}},
	}
	checkError(t, create(t, m), "")
	var got v1alpha1.TakeCareMecano
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(m), &got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.Level != v1alpha1.LevelApprentice {
		t.Errorf("level = %q, want Apprentice", got.Spec.Level)
	}
	if got.Spec.Schedule != "0 5 * * 1" {
		t.Errorf("schedule = %q, want Mondays at 05:00", got.Spec.Schedule)
	}
	mt := got.Spec.Maintenance
	for _, tc := range []struct {
		name string
		got  *metav1.Duration
		want time.Duration
	}{
		{"timeout", mt.Timeout, 15 * time.Minute},
		{"certificateWarning", mt.CertificateWarning, 30 * 24 * time.Hour},
	} {
		if tc.got == nil || tc.got.Duration != tc.want {
			t.Errorf("maintenance.%s = %v, want %v", tc.name, tc.got, tc.want)
		}
	}
}

func maintenanceFixture() *v1alpha1.TakeCareMaintenance {
	return &v1alpha1.TakeCareMaintenance{
		Name: uniqueName("maintenance"), Namespace: testNamespace,
		Spec: v1alpha1.TakeCareMaintenanceSpec{
			MecanoRef:  v1alpha1.RoleRef{Kind: v1alpha1.KindTakeCareMecano, Name: "mecano"},
			Trigger:    v1alpha1.MaintenanceTriggerSchedule,
			Components: []string{compBilling, "ghcr.io/acme/billing-api"},
			Level:      v1alpha1.LevelPractitioner,
		},
	}
}

func TestMaintenanceValidation(t *testing.T) {
	components := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("service/c%d", i)
		}
		return out
	}
	tests := []struct {
		name   string
		mutate func(*v1alpha1.TakeCareMaintenance)
		want   string
	}{
		{"scheduled maintenance", func(*v1alpha1.TakeCareMaintenance) {}, ""},
		{"on demand, nothing named", func(m *v1alpha1.TakeCareMaintenance) {
			m.Spec = v1alpha1.TakeCareMaintenanceSpec{MecanoRef: m.Spec.MecanoRef, Silent: true}
		}, ""},
		{"a trigger it does not know", func(m *v1alpha1.TakeCareMaintenance) { m.Spec.Trigger = "Investigation" }, "spec.trigger"},
		{"a review's kind", func(m *v1alpha1.TakeCareMaintenance) { m.Spec.MecanoRef.Kind = v1alpha1.KindTakeCareKine }, "a maintenance belongs to a TakeCareMecano"},
		{caseUnknownRoleKind, func(m *v1alpha1.TakeCareMaintenance) { m.Spec.MecanoRef.Kind = kindRanger }, wantKind},
		{"Mécano without a name", func(m *v1alpha1.TakeCareMaintenance) { m.Spec.MecanoRef.Name = "" }, wantName},
		{"20 components", func(m *v1alpha1.TakeCareMaintenance) { m.Spec.Components = components(20) }, ""},
		{"21 components", func(m *v1alpha1.TakeCareMaintenance) { m.Spec.Components = components(21) }, wantComponents},
		{"component past 253", func(m *v1alpha1.TakeCareMaintenance) { m.Spec.Components = []string{strings.Repeat("c", 254)} }, wantComponents},
		{"empty component", func(m *v1alpha1.TakeCareMaintenance) { m.Spec.Components = []string{""} }, wantComponents},
		{"unknown level", func(m *v1alpha1.TakeCareMaintenance) { m.Spec.Level = badLevel }, wantLevel},
		{caseNameAtBound, func(m *v1alpha1.TakeCareMaintenance) { m.Name = padded(m.Name, v1alpha1.MaxMaintenanceName) }, ""},
		{caseNamePastBound, func(m *v1alpha1.TakeCareMaintenance) {
			m.Name = padded(m.Name, v1alpha1.MaxMaintenanceName+1)
		}, v1alpha1.MessageMaintenanceName},
		{"name with a dot", func(m *v1alpha1.TakeCareMaintenance) { m.Name += ".v2" }, v1alpha1.MessageMaintenanceName},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := maintenanceFixture()
			tt.mutate(m)
			checkError(t, create(t, m), tt.want)
		})
	}
}

func maintenanceStatus() v1alpha1.TakeCareMaintenanceStatus {
	ends := metav1.NewTime(time.Date(2027, 2, 28, 0, 0, 0, 0, time.UTC))
	ev := []v1alpha1.Evidence{{Tool: "resources_list", Record: "demo-apps/billing-api"}}
	return v1alpha1.TakeCareMaintenanceStatus{
		Phase: v1alpha1.RunCompleted,
		Inventory: &v1alpha1.MaintenanceInventory{
			Components: []v1alpha1.InventoryComponent{{
				Key: compBilling, Live: "1.4.2", Declared: "1.5.0", Latest: "1.6.1", SupportEnds: &ends,
				Drift: v1alpha1.DriftVersion, Evidence: ev,
			}},
			APIs: []v1alpha1.DeprecatedAPIUse{{
				APIVersion: "policy/v1beta1", Kind: "PodDisruptionBudget", Namespace: nsDemoApps, Name: "billing-api",
				RemovedIn: "1.25", Replacement: "policy/v1", Where: []v1alpha1.APIWhere{v1alpha1.APIGit},
			}},
			Certificates: []v1alpha1.CertificateState{{Namespace: nsDemoApps, Name: "billing-tls", NotAfter: &ends}},
			GitOps:       []v1alpha1.GitOpsState{{Kind: "Application", Namespace: "argocd", Name: "shop", State: "OutOfSync", Revision: "3f2a1c0"}},
			Omitted:      v1alpha1.MaintenanceOmitted{Components: 4},
		},
		Findings: []v1alpha1.MaintenanceFinding{
			{ID: "RemovedAPI/policy/v1beta1/PodDisruptionBudget", Kind: v1alpha1.FindingRemovedAPI, Text: "The PDB manifest uses policy/v1beta1, removed in 1.25.", Severity: v1alpha1.SeverityNow, Evidence: ev},
			{ID: "Drift/" + compBilling, Kind: v1alpha1.FindingDrift, Component: compBilling, Text: "Git declares 1.5.0, 1.4.2 runs.", Severity: v1alpha1.SeveritySoon},
		},
		Plan: &v1alpha1.MaintenancePlan{
			Steps: []v1alpha1.PlanStep{{
				ID: "1", Text: "Move the PDB manifest to policy/v1.", Findings: []string{"RemovedAPI/policy/v1beta1/PodDisruptionBudget"},
				Class: v1alpha1.ActionReversible,
			}},
			Tested:       v1alpha1.PlanNotTested,
			PullRequests: []v1alpha1.PullRequestRef{{Repository: repoShop, URL: "https://github.com/acme/shop-deploy/pull/12", Branch: branchOfRecord}},
		},
		Counts:     &v1alpha1.MaintenanceCounts{Findings: 2, Now: 1, PullRequests: 1},
		BlindSpots: []v1alpha1.NotSeen{{Ref: "releases", Reason: "offline watch, current as of 2026-11-26"}},
		Published:  &v1alpha1.Publication{Connector: connReport, Destination: "platform-team", OK: true},
	}
}

// A maintenance is a record: its spec is written once, its status is
// what changes, within its bounds.
func TestMaintenanceIsARecord(t *testing.T) {
	ctx := context.Background()
	m := maintenanceFixture()
	m.Spec = v1alpha1.TakeCareMaintenanceSpec{MecanoRef: m.Spec.MecanoRef}
	checkError(t, create(t, m), "")
	var got v1alpha1.TakeCareMaintenance
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(m), &got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.Trigger != v1alpha1.MaintenanceTriggerOnDemand {
		t.Errorf("trigger = %q, want OnDemand by default", got.Spec.Trigger)
	}

	t.Run("the spec is immutable", func(t *testing.T) {
		changed := got.DeepCopy()
		changed.Spec.Silent = true
		checkError(t, k8sClient.Update(ctx, changed), "the spec of a maintenance is immutable")
	})

	findings := func(n int) []v1alpha1.MaintenanceFinding {
		out := make([]v1alpha1.MaintenanceFinding, n)
		for i := range out {
			out[i] = v1alpha1.MaintenanceFinding{ID: fmt.Sprintf("Behind/c%d", i), Kind: v1alpha1.FindingBehind, Text: "t", Severity: v1alpha1.SeverityLater}
		}
		return out
	}
	components := func(n int) []v1alpha1.InventoryComponent {
		out := make([]v1alpha1.InventoryComponent, n)
		for i := range out {
			out[i] = v1alpha1.InventoryComponent{Key: fmt.Sprintf("c%d", i)}
		}
		return out
	}
	certificates := func(n int) []v1alpha1.CertificateState {
		out := make([]v1alpha1.CertificateState, n)
		for i := range out {
			out[i] = v1alpha1.CertificateState{Namespace: nsDemoApps, Name: fmt.Sprintf("c%d", i)}
		}
		return out
	}
	steps := func(n int) []v1alpha1.PlanStep {
		out := make([]v1alpha1.PlanStep, n)
		for i := range out {
			out[i] = v1alpha1.PlanStep{ID: fmt.Sprint(i), Text: "t", Class: v1alpha1.ActionReversible}
		}
		return out
	}
	pullRequests := func(n int) []v1alpha1.PullRequestRef {
		out := make([]v1alpha1.PullRequestRef, n)
		for i := range out {
			out[i] = v1alpha1.PullRequestRef{Repository: fmt.Sprintf("acme/r%d", i), URL: "https://forge.example/pr", Branch: branchOfRecord}
		}
		return out
	}
	statusTests := []struct {
		name   string
		mutate func(*v1alpha1.TakeCareMaintenanceStatus)
		want   string
	}{
		{"a complete maintenance status", func(*v1alpha1.TakeCareMaintenanceStatus) {}, ""},
		{"unknown phase", func(st *v1alpha1.TakeCareMaintenanceStatus) { st.Phase = v1alpha1.PhaseTimedOut }, "phase must be"},
		{"200 components", func(st *v1alpha1.TakeCareMaintenanceStatus) { st.Inventory.Components = components(200) }, ""},
		{"201 components", func(st *v1alpha1.TakeCareMaintenanceStatus) { st.Inventory.Components = components(201) }, wantComponents},
		{"100 certificates", func(st *v1alpha1.TakeCareMaintenanceStatus) { st.Inventory.Certificates = certificates(100) }, ""},
		{"101 certificates", func(st *v1alpha1.TakeCareMaintenanceStatus) { st.Inventory.Certificates = certificates(101) }, "certificates"},
		{"unknown drift", func(st *v1alpha1.TakeCareMaintenanceStatus) { st.Inventory.Components[0].Drift = "Diverged" }, "drift"},
		{"an API used nowhere", func(st *v1alpha1.TakeCareMaintenanceStatus) { st.Inventory.APIs[0].Where = nil }, "where"},
		{"an API live and in Git", func(st *v1alpha1.TakeCareMaintenanceStatus) {
			st.Inventory.APIs[0].Where = []v1alpha1.APIWhere{v1alpha1.APILive, v1alpha1.APIGit}
		}, ""},
		{"an API in an unknown place", func(st *v1alpha1.TakeCareMaintenanceStatus) {
			st.Inventory.APIs[0].Where = []v1alpha1.APIWhere{"Backup"}
		}, "where"},
		{"a Flux Kustomization", func(st *v1alpha1.TakeCareMaintenanceStatus) { st.Inventory.GitOps[0].Kind = "Kustomization" }, ""},
		{"an unknown GitOps kind", func(st *v1alpha1.TakeCareMaintenanceStatus) { st.Inventory.GitOps[0].Kind = "HelmRelease" }, wantKind},
		{"negative omitted count", func(st *v1alpha1.TakeCareMaintenanceStatus) { st.Inventory.Omitted.APIs = -1 }, "apis"},
		{"50 findings", func(st *v1alpha1.TakeCareMaintenanceStatus) { st.Findings = findings(50) }, ""},
		{"51 findings", func(st *v1alpha1.TakeCareMaintenanceStatus) { st.Findings = findings(51) }, "findings"},
		{"duplicate finding id", func(st *v1alpha1.TakeCareMaintenanceStatus) { st.Findings[1].ID = st.Findings[0].ID }, wantDuplicate},
		{"unknown finding kind", func(st *v1alpha1.TakeCareMaintenanceStatus) { st.Findings[0].Kind = "Security" }, wantKind},
		{"unknown severity", func(st *v1alpha1.TakeCareMaintenanceStatus) { st.Findings[0].Severity = critical }, "findings[0].severity"},
		{"finding text at 512", func(st *v1alpha1.TakeCareMaintenanceStatus) {
			st.Findings[0].Text = strings.Repeat("t", v1alpha1.MaxFindingText)
		}, ""},
		{"finding text past 512", func(st *v1alpha1.TakeCareMaintenanceStatus) {
			st.Findings[0].Text = strings.Repeat("t", v1alpha1.MaxFindingText+1)
		}, wantTooLong},
		{"20 plan steps", func(st *v1alpha1.TakeCareMaintenanceStatus) { st.Plan.Steps = steps(20) }, ""},
		{"21 plan steps", func(st *v1alpha1.TakeCareMaintenanceStatus) { st.Plan.Steps = steps(21) }, "steps"},
		{"step text past 512", func(st *v1alpha1.TakeCareMaintenanceStatus) { st.Plan.Steps[0].Text = strings.Repeat("t", 513) }, wantTooLong},
		{"unknown action class", func(st *v1alpha1.TakeCareMaintenanceStatus) { st.Plan.Steps[0].Class = "safe" }, "class"},
		{"tested on a copy with evidence", func(st *v1alpha1.TakeCareMaintenanceStatus) {
			st.Plan.Tested, st.Plan.Evidence = v1alpha1.PlanOnCopy, []v1alpha1.Evidence{{Tool: "resources_list", Record: "staging/billing-api"}}
		}, ""},
		{"tested on a copy without evidence", func(st *v1alpha1.TakeCareMaintenanceStatus) {
			st.Plan.Tested = v1alpha1.PlanOnCopy
		}, "a plan tested on a copy names the copy's evidence"},
		{"16 pull requests", func(st *v1alpha1.TakeCareMaintenanceStatus) { st.Plan.PullRequests = pullRequests(16) }, ""},
		{"17 pull requests", func(st *v1alpha1.TakeCareMaintenanceStatus) { st.Plan.PullRequests = pullRequests(17) }, "pullRequests"},
		{"two pull requests on one repository", func(st *v1alpha1.TakeCareMaintenanceStatus) {
			st.Plan.PullRequests = append(st.Plan.PullRequests, st.Plan.PullRequests[0])
		}, wantDuplicate},
		{"a pull request off the Mécano's branches", func(st *v1alpha1.TakeCareMaintenanceStatus) {
			st.Plan.PullRequests[0].Branch = "main"
		}, "branch"},
		{"a pull request over http", func(st *v1alpha1.TakeCareMaintenanceStatus) {
			st.Plan.PullRequests[0].URL = "http://github.com/acme/shop-deploy/pull/12"
		}, "url"},
		{"21 blind spots", func(st *v1alpha1.TakeCareMaintenanceStatus) {
			for range 20 {
				st.BlindSpots = append(st.BlindSpots, v1alpha1.NotSeen{Ref: "r", Reason: "x"})
			}
		}, "blindSpots"},
		{"negative findings count", func(st *v1alpha1.TakeCareMaintenanceStatus) { st.Counts.Now = -1 }, "now"},
	}
	for _, tt := range statusTests {
		t.Run(tt.name, func(t *testing.T) {
			var cur v1alpha1.TakeCareMaintenance
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(m), &cur); err != nil {
				t.Fatal(err)
			}
			cur.Status = maintenanceStatus()
			tt.mutate(&cur.Status)
			checkError(t, k8sClient.Status().Update(ctx, &cur), tt.want)
		})
	}
}

func codeConnector() *v1alpha1.TakeCareConnector {
	return &v1alpha1.TakeCareConnector{
		Name: uniqueName(connPropose), Namespace: testNamespace,
		Spec: v1alpha1.TakeCareConnectorSpec{
			Types: []v1alpha1.ConnectorType{v1alpha1.ConnectorCode},
			MCP:   v1alpha1.MCPSpec{Addon: connPropose},
			Tools: []v1alpha1.ToolOverride{{Name: "propose_change", Class: v1alpha1.ToolPropose}},
			Propose: &v1alpha1.ProposeSpec{
				TokenSecretRef: v1alpha1.SecretRef{Name: "takecare-code-propose"},
				Repositories: []v1alpha1.ProposeRepository{
					{Forge: "github", APIURL: githubAPI, Repository: repoShop},
					{Forge: "gitlab", APIURL: "https://gitlab.example.org/api/v4", Repository: repoPlatform},
				},
			},
		},
	}
}

// The code connector type and its propose field (spec 12 §5, §6).
func TestCodeConnectorValidation(t *testing.T) {
	repos := func(n int) []v1alpha1.ProposeRepository {
		out := make([]v1alpha1.ProposeRepository, n)
		for i := range out {
			out[i] = v1alpha1.ProposeRepository{Forge: "github", APIURL: githubAPI, Repository: fmt.Sprintf("acme/r%d", i)}
		}
		return out
	}
	tests := []struct {
		name   string
		mutate func(*v1alpha1.TakeCareConnector)
		want   string
	}{
		{"propose connector", func(*v1alpha1.TakeCareConnector) {}, ""},
		{"read connector", func(c *v1alpha1.TakeCareConnector) {
			c.Spec.Propose, c.Spec.Tools = nil, []v1alpha1.ToolOverride{{Name: "file_get", Content: new(true)}}
		}, ""},
		{"propose on another type", func(c *v1alpha1.TakeCareConnector) {
			c.Spec.Types = []v1alpha1.ConnectorType{v1alpha1.ConnectorKnowledge}
		}, wantPropose},
		{"propose by url", func(c *v1alpha1.TakeCareConnector) {
			c.Spec.MCP = v1alpha1.MCPSpec{URL: "https://forge-mcp.example.org/mcp"}
		}, wantPropose},
		{"no repository", func(c *v1alpha1.TakeCareConnector) { c.Spec.Propose.Repositories = nil }, wantRepositories},
		{"16 repositories", func(c *v1alpha1.TakeCareConnector) { c.Spec.Propose.Repositories = repos(16) }, ""},
		{"17 repositories", func(c *v1alpha1.TakeCareConnector) { c.Spec.Propose.Repositories = repos(17) }, wantRepositories},
		{"repository twice", func(c *v1alpha1.TakeCareConnector) { c.Spec.Propose.Repositories[1].Repository = repoShop }, wantDuplicate},
		{"unknown forge", func(c *v1alpha1.TakeCareConnector) { c.Spec.Propose.Repositories[0].Forge = "gitea" }, "forge"},
		{"api over http", func(c *v1alpha1.TakeCareConnector) { c.Spec.Propose.Repositories[0].APIURL = "http://api.github.com" }, "apiURL"},
		{"repository without an owner", func(c *v1alpha1.TakeCareConnector) { c.Spec.Propose.Repositories[0].Repository = "shop" }, wantRepository},
		{"repository escaping its path", func(c *v1alpha1.TakeCareConnector) {
			c.Spec.Propose.Repositories[0].Repository = "acme/shop?ref=main"
		}, wantRepository},
		{"no token Secret", func(c *v1alpha1.TakeCareConnector) { c.Spec.Propose.TokenSecretRef.Name = "" }, wantName},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := codeConnector()
			tt.mutate(c)
			checkError(t, create(t, c), tt.want)
		})
	}
}

// A record names the Mécano as its role (RoleRef's enum); the Ranger's
// enum is checked with its other kinds (ranger_test.go).
func TestRoleRefTakesTheMecano(t *testing.T) {
	inv := &v1alpha1.TakeCareInvestigation{
		Name: uniqueName("inv"), Namespace: testNamespace,
		Spec: v1alpha1.TakeCareInvestigationSpec{
			RoleRef: v1alpha1.RoleRef{Kind: v1alpha1.KindTakeCareMecano, Name: "mecano"},
			Alert:   v1alpha1.AlertRecord{Name: "HighErrorRate"},
			Level:   v1alpha1.LevelApprentice,
		},
	}
	checkError(t, create(t, inv), "")
}
