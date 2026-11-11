// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package intendant

import (
	"context"
	"fmt"
	"slices"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/mcp"
	"github.com/agaetis-it/takecare-agents/internal/routes"
)

// mapTools are the map connector's tools the survey reads the Map with:
// map_at alone, the list and each entry at one revision.
var mapTools = []string{mcp.MapListTool}

// binding is what the Intendant's connectors give: the routes to render,
// and what is missing.
type binding struct {
	routes []routes.MCP
	// mapProblem says why the map connector cannot be read: the Intendant
	// is not ready without its Map.
	mapProblem string
	// problems name every optional connector that cannot be read.
	problems []string
	// mapRevision is the revision the map connector serves (its status).
	mapRevision string
}

// connectors inspects the bound connectors and sets ConnectorsReady: the
// map connector is required; the cluster and metrics connectors are
// optional, each one that cannot be read named in the condition.
func (r *Reconciler) connectors(ctx context.Context, in *v1alpha1.TakeCareIntendant, st *status) (binding, error) {
	var b binding
	c := in.Spec.Connectors
	for _, name := range c.Map {
		tools, problem, conn, err := usable(ctx, r.Client, in.Namespace, name, v1alpha1.ConnectorMap, mapTools, readTools)
		if err != nil {
			return b, err
		}
		if problem != "" {
			b.mapProblem = problem
			continue
		}
		b.mapRevision = conn.Status.MapRevision
		b.routes = append(b.routes, routes.MCP{Connector: name, Backend: "mcp-" + name, Tools: tools})
	}
	if len(c.Map) == 0 {
		b.mapProblem = "no map connector is given"
	}
	if len(c.Cluster) == 0 {
		b.problems = append(b.problems, "no cluster connector is given: nothing on a cluster is surveyed")
	}
	for _, name := range c.Cluster {
		tools, problem, _, err := usable(ctx, r.Client, in.Namespace, name, v1alpha1.ConnectorCluster, mcp.DiscoveryTools, stateTools)
		if err != nil {
			return b, err
		}
		if problem != "" {
			b.problems = append(b.problems, problem)
			continue
		}
		b.routes = append(b.routes, routes.MCP{Connector: name, Backend: "mcp-" + name, Tools: tools})
	}
	if len(c.Metrics) == 0 {
		b.problems = append(b.problems, "no metrics connector is given: the alerting rules are not surveyed")
	}
	for _, name := range c.Metrics {
		tools, problem, _, err := usable(ctx, r.Client, in.Namespace, name, v1alpha1.ConnectorMetrics, []string{mcp.RulesTool}, stateTools)
		if err != nil {
			return b, err
		}
		if problem != "" {
			b.problems = append(b.problems, problem)
			continue
		}
		// The gateway checks the tool's name only: agentgateway v1.6.0 does
		// not show a call's arguments to its rules, and a rule on them hides
		// the tool. get_rules' arguments are the client's check alone
		// (mcp.Endpoint.Allowed, spec 10 §8).
		b.routes = append(b.routes, routes.MCP{Connector: name, Backend: "mcp-" + name, Tools: tools})
	}
	switch {
	case b.mapProblem != "":
		st.set(v1alpha1.ConditionConnectorsReady, false, ReasonMapMissing, strings.Join(append([]string{b.mapProblem}, b.problems...), "; "))
	case len(b.problems) > 0:
		st.set(v1alpha1.ConditionConnectorsReady, false, ReasonConnectorDegrade, strings.Join(b.problems, "; "))
	default:
		st.set(v1alpha1.ConditionConnectorsReady, true, ReasonConnectorsReady, "the map, cluster and metrics connectors are ready")
	}
	return b, nil
}

// toolFilter says whether a discovered tool may be routed.
type toolFilter func(v1alpha1.ToolStatus) bool

func readTools(t v1alpha1.ToolStatus) bool { return t.Class == v1alpha1.ToolRead }

// stateTools are read and not content: what the survey may list without a
// payload (ADR 0014 §5).
func stateTools(t v1alpha1.ToolStatus) bool { return t.Class == v1alpha1.ToolRead && !t.Content }

// usable checks one connector: it exists, has the type, is Ready and
// exposes every wanted tool through the filter. It returns the tools to
// route and the connector, or the problem in one phrase.
func usable(ctx context.Context, c client.Reader, namespace, name string, typ v1alpha1.ConnectorType, want []string, ok toolFilter) ([]string, string, *v1alpha1.TakeCareConnector, error) {
	var conn v1alpha1.TakeCareConnector
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &conn); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Sprintf("connector %s does not exist", name), nil, nil
		}
		return nil, "", nil, fmt.Errorf("get connector %s: %w", name, err)
	}
	if !slices.Contains(conn.Spec.Types, typ) {
		return nil, fmt.Sprintf("connector %s is not of type %s", name, typ), nil, nil
	}
	if !meta.IsStatusConditionTrue(conn.Status.Conditions, v1alpha1.ConditionReady) {
		return nil, fmt.Sprintf("connector %s is not Ready", name), nil, nil
	}
	var missing []string
	for _, w := range want {
		i := slices.IndexFunc(conn.Status.Tools, func(t v1alpha1.ToolStatus) bool { return t.Name == w })
		if i < 0 || !ok(conn.Status.Tools[i]) {
			missing = append(missing, w)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Sprintf("connector %s does not expose %s as a read tool", name, strings.Join(missing, ", ")), nil, nil
	}
	return want, "", &conn, nil
}
