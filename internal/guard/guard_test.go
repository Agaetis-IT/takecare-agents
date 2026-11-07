// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package guard

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// rights answers the probes from a set of "namespace/group/resource/subresource/verb".
func rights(allowed ...string) Can {
	set := map[string]bool{}
	for _, a := range allowed {
		set[a] = true
	}
	return func(_ context.Context, ns, group, resource, subresource, verb string) (bool, error) {
		return set[ns+"/"+group+"/"+resource+"/"+subresource+"/"+verb], nil
	}
}

func TestCheck(t *testing.T) {
	listShop := "shop//pods//list"
	for _, tc := range []struct {
		name string
		can  Can
		want Verdict
	}{
		{"read-only and pods listed", rights(listShop), Verdict{Violations: []string{}, Missing: []string{}}},
		{"pods not listed in the namespace", rights(), Verdict{Violations: []string{}, Missing: []string{"list pods in shop"}}},
		{"Secrets cluster-wide and exec in the namespace", rights(listShop, "//secrets//get", "shop//pods/exec/create"),
			Verdict{Violations: []string{"read Secrets cluster-wide", "exec into pods in shop"}, Missing: []string{}}},
		{"a wildcard on verbs", rights(listShop, "shop//pods//takecare-guard-probe"),
			Verdict{Violations: []string{"a wildcard on verbs in shop"}, Missing: []string{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Check(context.Background(), tc.can, []string{"shop"})
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("verdict (-want +got):\n%s", diff)
			}
		})
	}
	failing := func(context.Context, string, string, string, string, string) (bool, error) {
		return false, errors.New("forbidden")
	}
	if _, err := Check(context.Background(), failing, nil); err == nil {
		t.Error("an API error must be returned, not read as a denial")
	}
}
