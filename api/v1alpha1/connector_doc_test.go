// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package v1alpha1_test

import (
	"context"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

func clusterConnector() *v1alpha1.TakeCareConnector {
	return &v1alpha1.TakeCareConnector{
		Name: uniqueName(addonKubernetes), Namespace: testNamespace,
		Spec: v1alpha1.TakeCareConnectorSpec{
			Types: []v1alpha1.ConnectorType{v1alpha1.ConnectorCluster, v1alpha1.ConnectorLogs},
			MCP:   v1alpha1.MCPSpec{Addon: addonKubernetes},
			Kubernetes: &v1alpha1.KubernetesSpec{
				Namespaces:     []v1alpha1.Namespace{nsDemoApps, nsAckSystem},
				ExtraResources: []v1alpha1.ResourceRef{{Group: "rds.services.k8s.aws", Resource: "dbproxies"}},
			},
		},
	}
}

func reportConnector() *v1alpha1.TakeCareConnector {
	return &v1alpha1.TakeCareConnector{
		Name: uniqueName("report"), Namespace: testNamespace,
		Spec: v1alpha1.TakeCareConnectorSpec{
			Types: []v1alpha1.ConnectorType{v1alpha1.ConnectorReport},
			MCP:   v1alpha1.MCPSpec{Addon: "report-slack"},
			Report: &v1alpha1.ReportSpec{
				Adapter: "slack",
				Destinations: []v1alpha1.Destination{
					{Name: destOnCall, Target: "#on-call"},
					{Name: "review", Target: "#takecare-review"},
				},
			},
		},
	}
}

func TestConnectorValidation(t *testing.T) {
	const needsHTTPS = "cluster needs an https mcp.url"
	remote := func(c *v1alpha1.TakeCareConnector) {
		c.Spec.MCP = v1alpha1.MCPSpec{URL: "https://east.example.internal:8443/mcp"}
		c.Spec.Cluster = &v1alpha1.ClusterSpec{Name: "east", TLS: v1alpha1.ClusterTLS{
			CASecretRef: v1alpha1.SecretRef{Name: "east-ca"}, ClientCertSecretRef: v1alpha1.SecretRef{Name: "east-client"}}}
	}
	tests := []struct {
		name   string
		base   func() *v1alpha1.TakeCareConnector
		mutate func(*v1alpha1.TakeCareConnector)
		want   string
	}{
		{"cluster connector", clusterConnector, func(*v1alpha1.TakeCareConnector) {}, ""},
		{"report connector", reportConnector, func(*v1alpha1.TakeCareConnector) {}, ""},
		{"remote cluster", clusterConnector, remote, ""},
		{"remote cluster on an add-on", clusterConnector, func(c *v1alpha1.TakeCareConnector) {
			remote(c)
			c.Spec.MCP = v1alpha1.MCPSpec{Addon: addonKubernetes}
		}, needsHTTPS},
		{"remote cluster over http", clusterConnector, func(c *v1alpha1.TakeCareConnector) {
			remote(c)
			c.Spec.MCP.URL = "http://east.example.internal:8443/mcp"
		}, needsHTTPS},
		{"remote cluster without the cluster type", clusterConnector, func(c *v1alpha1.TakeCareConnector) {
			remote(c)
			c.Spec.Types = []v1alpha1.ConnectorType{v1alpha1.ConnectorLogs}
			c.Spec.Kubernetes = nil
		}, needsHTTPS},
		{"remote cluster name not a DNS label", clusterConnector, func(c *v1alpha1.TakeCareConnector) {
			remote(c)
			c.Spec.Cluster.Name = "East_1"
		}, "cluster.name"},
		{"external server with a header from a Secret", clusterConnector, func(c *v1alpha1.TakeCareConnector) {
			c.Spec.Types = []v1alpha1.ConnectorType{v1alpha1.ConnectorTickets}
			c.Spec.Kubernetes = nil
			c.Spec.MCP = v1alpha1.MCPSpec{
				URL:         "https://mcp.example.com/mcp",
				HeadersFrom: []v1alpha1.HeaderFrom{{Name: "Authorization", SecretKeyRef: v1alpha1.SecretKeyRef{Name: "example-mcp", Key: "token"}}},
			}
		}, ""},
		{"tool class overrides", clusterConnector, func(c *v1alpha1.TakeCareConnector) {
			c.Spec.Tools = []v1alpha1.ToolOverride{{Name: toolPodsLog, Class: v1alpha1.ToolRead}}
		}, ""},
		{"no type", clusterConnector, func(c *v1alpha1.TakeCareConnector) { c.Spec.Types = nil }, "types"},
		{"unknown type", clusterConnector, func(c *v1alpha1.TakeCareConnector) {
			c.Spec.Types = []v1alpha1.ConnectorType{"chat"}
		}, "types"},
		{"report mixed with another type", reportConnector, func(c *v1alpha1.TakeCareConnector) {
			c.Spec.Types = append(c.Spec.Types, v1alpha1.ConnectorLogs)
		}, "no other type"},
		{"report without spec.report", reportConnector, func(c *v1alpha1.TakeCareConnector) { c.Spec.Report = nil }, "must declare spec.report"},
		{"spec.report on a metrics connector", clusterConnector, func(c *v1alpha1.TakeCareConnector) {
			c.Spec.Types = []v1alpha1.ConnectorType{v1alpha1.ConnectorMetrics}
			c.Spec.Kubernetes = nil
			c.Spec.Report = reportConnector().Spec.Report
		}, "only valid for a connector of type report"},
		{"unknown report adapter", reportConnector, func(c *v1alpha1.TakeCareConnector) { c.Spec.Report.Adapter = "teams" }, "adapter"},
		{"report without destination", reportConnector, func(c *v1alpha1.TakeCareConnector) { c.Spec.Report.Destinations = nil }, "destinations"},
		{"duplicate destination", reportConnector, func(c *v1alpha1.TakeCareConnector) {
			c.Spec.Report.Destinations = append(c.Spec.Report.Destinations, v1alpha1.Destination{Name: destOnCall, Target: "#other"})
		}, "Duplicate value"},
		{"cluster without spec.kubernetes", clusterConnector, func(c *v1alpha1.TakeCareConnector) { c.Spec.Kubernetes = nil }, "must declare spec.kubernetes"},
		{"spec.kubernetes on a metrics connector", clusterConnector, func(c *v1alpha1.TakeCareConnector) {
			c.Spec.Types = []v1alpha1.ConnectorType{v1alpha1.ConnectorMetrics}
		}, "only valid for a connector of type cluster"},
		{"cluster without namespace", clusterConnector, func(c *v1alpha1.TakeCareConnector) { c.Spec.Kubernetes.Namespaces = nil }, wantNamespace},
		{badNamespace, clusterConnector, func(c *v1alpha1.TakeCareConnector) {
			c.Spec.Kubernetes.Namespaces = []v1alpha1.Namespace{"Demo Apps"}
		}, wantNamespace},
		{"both addon and url", clusterConnector, func(c *v1alpha1.TakeCareConnector) { c.Spec.MCP.URL = "https://mcp.example.com/mcp" }, "exactly one of addon and url"},
		{"neither addon nor url", clusterConnector, func(c *v1alpha1.TakeCareConnector) { c.Spec.MCP = v1alpha1.MCPSpec{} }, "exactly one of addon and url"},
		{"url without scheme", clusterConnector, func(c *v1alpha1.TakeCareConnector) { c.Spec.MCP = v1alpha1.MCPSpec{URL: "mcp.example.com"} }, wantURL},
		{"headersFrom with an add-on", clusterConnector, func(c *v1alpha1.TakeCareConnector) {
			c.Spec.MCP.HeadersFrom = []v1alpha1.HeaderFrom{{Name: "X", SecretKeyRef: v1alpha1.SecretKeyRef{Name: "s", Key: "k"}}}
		}, "only valid with url"},
		{"configmaps as an explicit extra resource", clusterConnector, func(c *v1alpha1.TakeCareConnector) {
			c.Spec.Kubernetes.ExtraResources = []v1alpha1.ResourceRef{{Resource: "configmaps"}}
		}, ""},
		{"secrets as an extra resource", clusterConnector, func(c *v1alpha1.TakeCareConnector) {
			c.Spec.Kubernetes.ExtraResources = []v1alpha1.ResourceRef{{Resource: "secrets"}}
		}, "read-only guard"},
		{"pods/exec as an extra resource", clusterConnector, func(c *v1alpha1.TakeCareConnector) {
			c.Spec.Kubernetes.ExtraResources = []v1alpha1.ResourceRef{{Resource: "pods/exec"}}
		}, "read-only guard"},
		{"map connector", clusterConnector, func(c *v1alpha1.TakeCareConnector) {
			c.Spec.Types = []v1alpha1.ConnectorType{v1alpha1.ConnectorMap}
			c.Spec.Kubernetes = nil
		}, ""},
		{"tool marked content", clusterConnector, func(c *v1alpha1.TakeCareConnector) {
			content := true
			c.Spec.Tools = []v1alpha1.ToolOverride{{Name: toolPodsLog, Class: v1alpha1.ToolRead, Content: &content}}
		}, ""},
		{"tool marked content without a class", clusterConnector, func(c *v1alpha1.TakeCareConnector) {
			content := true
			c.Spec.Tools = []v1alpha1.ToolOverride{{Name: toolPodsLog, Content: &content}}
		}, ""},
		{"tool override setting nothing", clusterConnector, func(c *v1alpha1.TakeCareConnector) {
			c.Spec.Tools = []v1alpha1.ToolOverride{{Name: toolPodsLog}}
		}, "sets a class, a content mark, or both"},
		{"unknown tool class", clusterConnector, func(c *v1alpha1.TakeCareConnector) {
			c.Spec.Tools = []v1alpha1.ToolOverride{{Name: toolPodsLog, Class: "write"}}
		}, "class"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := tt.base()
			tt.mutate(c)
			checkError(t, create(t, c), tt.want)
		})
	}
}

func docFixture() *v1alpha1.TakeCareDoc {
	return &v1alpha1.TakeCareDoc{
		Name: uniqueName("doc"), Namespace: testNamespace,
		Spec: v1alpha1.TakeCareDocSpec{
			RoleCommonSpec: v1alpha1.RoleCommonSpec{
				Level: v1alpha1.LevelPractitioner,
				Scope: v1alpha1.Scope{Namespaces: []v1alpha1.Namespace{nsDemoApps}},
			},
			Connectors: v1alpha1.DocConnectors{
				Cluster:   []string{addonKubernetes},
				Metrics:   []string{"prometheus"},
				Knowledge: []string{"incidents", "runbooks", "vendor"},
				Report:    connReport,
			},
			Report: v1alpha1.ReportDestinations{Apprentice: "review", Practitioner: destOnCall},
			Alerts: v1alpha1.AlertsSpec{Matchers: []v1alpha1.AlertMatcher{{Name: "severity", Value: critical}}},
		},
	}
}

func TestDocValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*v1alpha1.TakeCareDoc)
		want   string
	}{
		{"complete doc", func(*v1alpha1.TakeCareDoc) {}, ""},
		{"minimal doc", func(d *v1alpha1.TakeCareDoc) {
			d.Spec = v1alpha1.TakeCareDocSpec{Scope: d.Spec.Scope}
		}, ""},
		{"Expert is accepted by the schema", func(d *v1alpha1.TakeCareDoc) { d.Spec.Level = v1alpha1.LevelExpert }, ""},
		{"model override", func(d *v1alpha1.TakeCareDoc) { d.Spec.Model = &v1alpha1.ModelRef{Name: "local"} }, ""},
		{"suspended", func(d *v1alpha1.TakeCareDoc) { d.Spec.Suspend = true }, ""},
		{caseUnknownLevel, func(d *v1alpha1.TakeCareDoc) { d.Spec.Level = badLevel }, wantLevel},
		{"the old value trial is rejected", func(d *v1alpha1.TakeCareDoc) { d.Spec.Level = "trial" }, wantLevel},
		{"empty scope", func(d *v1alpha1.TakeCareDoc) { d.Spec.Scope.Namespaces = nil }, "at least one selector"},
		{"entities without namespaces: routed by entity", func(d *v1alpha1.TakeCareDoc) {
			d.Spec.Scope = v1alpha1.Scope{Entities: []v1alpha1.EntityKey{entityBilling}}
		}, ""},
		{"namespaces and entities", func(d *v1alpha1.TakeCareDoc) {
			d.Spec.Scope.Entities = []v1alpha1.EntityKey{entityBilling, "datastore/billing-db"}
		}, ""},
		{"entity key with capitals", func(d *v1alpha1.TakeCareDoc) { d.Spec.Scope.Entities = []v1alpha1.EntityKey{"Service/Billing"} }, wantEntities},
		{"entity key with an unknown type", func(d *v1alpha1.TakeCareDoc) { d.Spec.Scope.Entities = []v1alpha1.EntityKey{"pod/x"} }, wantEntities},
		{"entity key without a name", func(d *v1alpha1.TakeCareDoc) { d.Spec.Scope.Entities = []v1alpha1.EntityKey{"service/"} }, wantEntities},
		{"content tools", func(d *v1alpha1.TakeCareDoc) { d.Spec.ContentTools = []string{toolPodsLog} }, ""},
		{badNamespace, func(d *v1alpha1.TakeCareDoc) {
			d.Spec.Scope.Namespaces = []v1alpha1.Namespace{"Demo_Apps"}
		}, wantNamespace},
		{"model name not a DNS label", func(d *v1alpha1.TakeCareDoc) { d.Spec.Model = &v1alpha1.ModelRef{Name: "Local Model"} }, "model"},
		{"negative concurrency", func(d *v1alpha1.TakeCareDoc) { d.Spec.Investigation.MaxConcurrent = -1 }, "maxConcurrent"},
		{"matcher without name", func(d *v1alpha1.TakeCareDoc) {
			d.Spec.Alerts.Matchers = []v1alpha1.AlertMatcher{{Value: critical}}
		}, wantName},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := docFixture()
			tt.mutate(d)
			checkError(t, create(t, d), tt.want)
		})
	}
}

func TestDocDefaults(t *testing.T) {
	d := docFixture()
	d.Spec = v1alpha1.TakeCareDocSpec{Scope: d.Spec.Scope}
	checkError(t, create(t, d), "")
	var got v1alpha1.TakeCareDoc
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(d), &got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.Level != v1alpha1.LevelApprentice {
		t.Errorf("level = %q, want Apprentice", got.Spec.Level)
	}
	inv := got.Spec.Investigation
	if inv.Timeout == nil || inv.Timeout.Duration != 5*time.Minute {
		t.Errorf("timeout = %v, want 5m", inv.Timeout)
	}
	if inv.Cooldown == nil || inv.Cooldown.Duration != 15*time.Minute {
		t.Errorf("cooldown = %v, want 15m", inv.Cooldown)
	}
	if inv.MaxConcurrent != 1 {
		t.Errorf("maxConcurrent = %d, want 1", inv.MaxConcurrent)
	}
}
