// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package mecano

import (
	"strings"
	"testing"

	"github.com/google/cel-go/cel"

	"github.com/agaetis-it/takecare-agents/internal/routes"
)

const (
	repoCharts = "group/sub/charts"
	branchOK   = "takecare/mecano/maintenance-0a1b2c3d4e"
)

// proposeArgs are a valid call's arguments, changed by mutate.
func proposeArgs(mutate func(map[string]any)) map[string]any {
	args := map[string]any{
		"repository": repoDeploy, "branch": branchOK, "title": "Upgrade", "body": "Plan",
		"files": []any{map[string]any{"path": "deploy/web.yaml", "content": "x"}},
	}
	if mutate != nil {
		mutate(args)
	}
	return args
}

// The gateway's rule on propose_change's arguments (spec 12 §6.4), as the
// route renders it (routes.CallsExpression). cel-go stands in for the
// gateway's own CEL engine; an evaluation error counts as a refusal, as
// the gateway answers one.
func TestProposeCallRule(t *testing.T) {
	env, err := cel.NewEnv(cel.Variable("mcp", cel.DynType), cel.Variable("request", cel.DynType))
	if err != nil {
		t.Fatal(err)
	}
	ast, iss := env.Compile(routes.CallsExpression(ProposeCallRule([]string{repoCharts, repoDeploy})))
	if iss.Err() != nil {
		t.Fatal(iss.Err())
	}
	prg, err := env.Program(ast)
	if err != nil {
		t.Fatal(err)
	}
	admits := func(method string, m map[string]any) bool {
		out, _, err := prg.Eval(map[string]any{"mcp": m, "request": map[string]any{"method": method}})
		if err != nil {
			return false
		}
		v, ok := out.Value().(bool)
		return ok && v
	}
	call := func(tool string, args map[string]any) map[string]any {
		return map[string]any{"methodName": "tools/call", "tool": map[string]any{keyName: tool, "arguments": args}}
	}
	for _, tc := range []struct {
		name string
		mcp  map[string]any
		want bool
	}{
		{"a valid call", call("propose_change", proposeArgs(nil)), true},
		{"the other listed repository", call("propose_change", proposeArgs(func(a map[string]any) { a["repository"] = repoCharts })), true},
		{"a branch of 63 characters after the prefix", call("propose_change", proposeArgs(func(a map[string]any) {
			a["branch"] = "takecare/mecano/" + strings.Repeat("a", 63)
		})), true},
		{"tools/list, no tool", map[string]any{"methodName": "tools/list"}, true},
		{"an unlisted repository", call("propose_change", proposeArgs(func(a map[string]any) { a["repository"] = repoOther })), false},
		{"a repository as a list", call("propose_change", proposeArgs(func(a map[string]any) { a["repository"] = []any{repoDeploy} })), false},
		{"no repository", call("propose_change", proposeArgs(func(a map[string]any) { delete(a, "repository") })), false},
		{"a branch outside the prefix", call("propose_change", proposeArgs(func(a map[string]any) { a["branch"] = "main" })), false},
		{"a branch under the prefix's parent", call("propose_change", proposeArgs(func(a map[string]any) { a["branch"] = "takecare/other" })), false},
		{"a branch with a slash below the prefix", call("propose_change", proposeArgs(func(a map[string]any) { a["branch"] = "takecare/mecano/a/b" })), false},
		{"a branch of 64 characters after the prefix", call("propose_change", proposeArgs(func(a map[string]any) {
			a["branch"] = "takecare/mecano/" + strings.Repeat("a", 64)
		})), false},
		{"a branch in upper case", call("propose_change", proposeArgs(func(a map[string]any) { a["branch"] = "takecare/mecano/Fix" })), false},
		{"a branch starting with a hyphen", call("propose_change", proposeArgs(func(a map[string]any) { a["branch"] = "takecare/mecano/-x" })), false},
		{"a branch with a line break", call("propose_change", proposeArgs(func(a map[string]any) { a["branch"] = "takecare/mecano/x\nmain" })), false},
		{"no branch", call("propose_change", proposeArgs(func(a map[string]any) { delete(a, "branch") })), false},
		{"an argument outside the five", call("propose_change", proposeArgs(func(a map[string]any) { a["force"] = true })), false},
		{"another tool", call("merge_pull_request", proposeArgs(nil)), false},
		{"a POST the gateway did not read as MCP", map[string]any{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := admits("POST", tc.mcp); got != tc.want {
				t.Errorf("the rule admits %v: %v, want %v", tc.mcp, got, tc.want)
			}
		})
	}
	// The MCP client's GET (its stream) and DELETE (its close) carry no
	// call; a PUT does not reach the server.
	if !admits("GET", map[string]any{}) || !admits("DELETE", map[string]any{}) || admits("PUT", map[string]any{}) {
		t.Error("the rule's request guard is not routes.MCPRequest's")
	}
}

// The rule's text is exactly the one approved at the build (spec 12 §12):
// a change to it shows here.
func TestProposeCallRuleText(t *testing.T) {
	want := `!has(mcp.tool) || (mcp.tool.name == "propose_change" && ` +
		`mcp.tool.arguments.all(k, k in ["repository", "branch", "title", "body", "files"]) && ` +
		`mcp.tool.arguments.repository in ["group/sub/charts", "team/deploy"] && ` +
		`mcp.tool.arguments.branch.matches("^takecare/mecano/[a-z0-9][a-z0-9-]{0,62}$"))`
	if got := ProposeCallRule([]string{repoCharts, repoDeploy}); got != want {
		t.Errorf("rule\n got %s\nwant %s", got, want)
	}
}
