// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package intendant

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/cel-go/cel"

	"github.com/agaetis-it/takecare-agents/internal/mcp"
)

// The gateway's rule on get_rules' arguments against the table the
// operator's client check is tested with (internal/mcp/testdata), so the
// two cannot drift. cel-go stands in for the gateway's own CEL engine; an
// evaluation error counts as a refusal, as the gateway answers one. The
// chart smoke runs the same table against the pinned gateway, which is
// the proof for the error cases.
func TestRulesCallCELTable(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "mcp", "testdata", "rules-args.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name string         `json:"name"`
		Args map[string]any `json:"args"`
		Want bool           `json:"want"`
	}
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("rules-args.json holds no case")
	}
	env, err := cel.NewEnv(cel.Variable("mcp", cel.DynType))
	if err != nil {
		t.Fatal(err)
	}
	ast, iss := env.Compile(RulesCallCEL)
	if iss.Err() != nil {
		t.Fatal(iss.Err())
	}
	prg, err := env.Program(ast)
	if err != nil {
		t.Fatal(err)
	}
	// toolCall is the gateway's mcp variable on a tools/call.
	toolCall := func(tool string, args map[string]any) map[string]any {
		return map[string]any{"tool": map[string]any{"name": tool, "arguments": args}}
	}
	admits := func(request map[string]any) bool {
		out, _, err := prg.Eval(map[string]any{"mcp": request})
		if err != nil {
			return false
		}
		v, ok := out.Value().(bool)
		return ok && v
	}
	for _, tt := range cases {
		t.Run(tt.Name, func(t *testing.T) {
			if got := admits(toolCall(mcp.RulesTool, tt.Args)); got != tt.Want {
				t.Errorf("the rule admits %v: %v, want %v", tt.Args, got, tt.Want)
			}
		})
	}
	// A request without a tool (initialize, tools/list) passes, and another
	// tool is not this rule's concern.
	if !admits(map[string]any{}) || !admits(toolCall("get_targets", map[string]any{})) {
		t.Error("the rule refuses a request that is not a get_rules call")
	}
}
