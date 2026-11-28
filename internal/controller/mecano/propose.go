// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package mecano

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	mecanoagent "github.com/agaetis-it/takecare-agents/internal/mecano"
	"github.com/agaetis-it/takecare-agents/internal/roleagent"
	"github.com/agaetis-it/takecare-agents/internal/routes"
)

// BranchPattern is the branch a proposal may name (spec 12 §6.2), as the
// propose server checks it: the gateway checks it again on the route.
const BranchPattern = `^takecare/mecano/[a-z0-9][a-z0-9-]{0,62}$`

// proposeArguments are the only arguments propose_change takes (spec 12
// §6.2, §6.4).
var proposeArguments = []string{"repository", "branch", "title", "body", "files"}

// ProposeCallRule is the gateway's rule on propose_change's arguments, on
// the propose route itself (spec 12 §6.4): a route-level authorization,
// the one place agentgateway v1.7.0 shows a call's arguments
// (mcp.tool.arguments is not set in MCP authorization). Only
// propose_change, no argument outside the five, a repository among the
// Mécano's maintenance.repositories on that connector, and a branch under
// takecare/mecano/. A request without a tool (initialize, tools/list) has
// no mcp.tool and passes. A missing argument or one of the wrong type makes
// the expression an error, which the gateway answers with a refusal.
// repositories is never empty: without one, no route is rendered.
func ProposeCallRule(repositories []string) string {
	quote := func(list []string) string {
		quoted := make([]string, len(list))
		for i, s := range list {
			quoted[i] = strconv.Quote(s)
		}
		return "[" + strings.Join(quoted, ", ") + "]"
	}
	return `!has(mcp.tool) || (mcp.tool.name == ` + strconv.Quote(mecanoagent.ProposeTool) + ` && ` +
		`mcp.tool.arguments.all(k, k in ` + quote(proposeArguments) + `) && ` +
		`mcp.tool.arguments.repository in ` + quote(repositories) + ` && ` +
		`mcp.tool.arguments.branch.matches(` + strconv.Quote(BranchPattern) + `))`
}

// pullRequestsWithout is the end of a propose line: what a maintenance does
// while the propose route is not usable (spec 12 §2).
const pullRequestsWithout = "each plan is recorded and published without a pull request"

// propose binds the propose slot (spec 12 §6.4): at Practitioner and
// above only, with maintenance.repositories on that connector, each one
// the connector lists. The route carries the argument rule from the
// start; its allow-list admits propose_change, and the agent is given it,
// only once the gateway has Accepted and Attached that very rule, at the
// policy's current generation, and while the Mécano is not suspended:
// fail closed.
// Whatever keeps the route from proposing is a blind spot, never a
// refusal: the Mécano stays Ready and publishes its plans without a pull
// request.
func (r *Reconciler) propose(ctx context.Context, m *v1alpha1.TakeCareMecano, level v1alpha1.Level, role routes.Role, b *binding) error {
	names := m.Spec.Connectors.Propose
	if level == v1alpha1.LevelApprentice {
		return nil
	}
	if len(names) == 0 {
		b.unavailable = append(b.unavailable, "no propose connector: "+pullRequestsWithout)
		return nil
	}
	name := names[0]
	var wanted []string
	for _, repo := range m.Spec.Maintenance.Repositories {
		if repo.Connector == name {
			wanted = append(wanted, repo.Repository)
		}
	}
	if len(wanted) == 0 {
		b.unavailable = append(b.unavailable, fmt.Sprintf("maintenance.repositories names no repository of the propose connector %s: %s", name, pullRequestsWithout))
		return nil
	}
	if b.routed(name) {
		b.unavailable = append(b.unavailable, fmt.Sprintf("connector %s is bound as propose and in another slot: its route would carry read tools under the propose rule; %s", name, pullRequestsWithout))
		return nil
	}
	c, err := r.connector(ctx, m.Namespace, name)
	switch {
	case err != nil:
		return err
	case c == nil:
		b.unavailable = append(b.unavailable, fmt.Sprintf("propose connector %s does not exist: %s", name, pullRequestsWithout))
		return nil
	case !slices.Contains(c.Spec.Types, v1alpha1.ConnectorCode) || c.Spec.Propose == nil:
		b.unavailable = append(b.unavailable, fmt.Sprintf("connector %s is bound as propose but is not a propose add-on (type code with spec.propose): %s", name, pullRequestsWithout))
		return nil
	}
	var listed, unlisted []string
	for _, repo := range wanted {
		if slices.ContainsFunc(c.Spec.Propose.Repositories, func(p v1alpha1.ProposeRepository) bool { return p.Repository == repo }) {
			listed = append(listed, repo)
		} else {
			unlisted = append(unlisted, repo)
		}
	}
	if len(unlisted) > 0 {
		b.unavailable = append(b.unavailable, fmt.Sprintf("the propose connector %s does not list %s: no pull request there", name, strings.Join(unlisted, ", ")))
	}
	if len(listed) == 0 {
		return nil
	}
	slices.Sort(listed)
	if !slices.Contains(roleagent.ProposeTools(c, level, roleagent.ProposeSlot), mecanoagent.ProposeTool) {
		b.unavailable = append(b.unavailable, fmt.Sprintf("the propose connector %s offers no %s classed propose: %s", name, mecanoagent.ProposeTool, pullRequestsWithout))
		return nil
	}
	// The verdict read is on the rule rendered now: a policy that still
	// carries the rule of other repositories, or a verdict on an older
	// generation, keeps the route closed until the gateway has read the
	// rule this reconcile renders.
	rule := ProposeCallRule(listed)
	enforced, problem, err := routes.CallRuleEnforced(ctx, r.Client, role, name, rule)
	if err != nil {
		return err
	}
	var admitted []string
	switch {
	case m.Spec.Suspend:
		// Conservative: a suspended Mécano runs no maintenance, so its
		// write tool is not left open on the gateway meanwhile.
		b.unavailable = append(b.unavailable, "spec.suspend is set: the propose route admits nothing")
	case enforced:
		admitted = []string{mecanoagent.ProposeTool}
		b.agent[name] = admitted
	default:
		b.unavailable = append(b.unavailable, problem+": "+pullRequestsWithout+" until the gateway enforces it")
	}
	b.routes = append(b.routes, routes.MCP{Connector: name, Backend: "mcp-" + name, Tools: admitted, CallRule: rule})
	if !meta.IsStatusConditionTrue(c.Status.Conditions, v1alpha1.ConditionReady) {
		b.unavailable = append(b.unavailable, fmt.Sprintf("propose connector %s is not Ready", name))
	}
	return nil
}
