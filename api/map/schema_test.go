// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package mapschema

import "testing"

func TestParseAzure(t *testing.T) {
	const (
		sub   = "/subscriptions/1111AAAA-1111-1111-1111-111111111111"
		lower = "1111aaaa-1111-1111-1111-111111111111"
		shop  = "rg-shop"
	)
	for _, tc := range []struct {
		id   string
		want AzureScope
		ok   bool
	}{
		{sub, AzureScope{Subscription: lower}, true},
		{sub + "/resourceGroups/RG-Shop", AzureScope{lower, shop}, true},
		{sub + "/resourcegroups/rg-shop/providers/Microsoft.Compute/virtualMachines/vm-web", AzureScope{lower, shop}, true},
		{sub + "/resourceGroups/rg-shop/providers/Microsoft.Sql/servers/sql-1/databases/orders", AzureScope{lower, shop}, true},
		// An extension resource keeps the first resource group.
		{sub + "/resourceGroups/rg-shop/providers/Microsoft.Web/sites/web/providers/Microsoft.Insights/diagnosticSettings/ds", AzureScope{lower, shop}, true},
		{"/SUBSCRIPTIONS/" + lower + "/providers/Microsoft.Network/dnszones/shop.example", AzureScope{Subscription: lower}, true},
		{sub + "/", AzureScope{}, false},
		{sub + "/resourceGroups/rg-shop/", AzureScope{}, false},
		{sub + "//resourceGroups/rg-shop", AzureScope{}, false},
		{sub + "/resourceGroups", AzureScope{}, false},
		{sub + "/resourceGroups/rg-shop/providers/Microsoft.Compute/virtualMachines", AzureScope{}, false},
		{sub + "/providers/Microsoft.Network", AzureScope{}, false},
		{sub + "/locations/westeurope", AzureScope{}, false},
		{"subscriptions/" + lower, AzureScope{}, false},
		{"/subscriptions", AzureScope{}, false},
		{"", AzureScope{}, false},
	} {
		got, ok := ParseAzure(tc.id)
		if got != tc.want || ok != tc.ok {
			t.Errorf("ParseAzure(%q) = %+v, %v; want %+v, %v", tc.id, got, ok, tc.want, tc.ok)
		}
	}
}
