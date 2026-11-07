// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"slices"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	files := []string{"--tls-cert-file=c", "--tls-key-file=k", "--client-ca-file=ca"}
	for _, tc := range []struct {
		name string
		args []string
		want string // an error containing it, or none
	}{
		{"complete", append(slices.Clone(files), "--client-names=a.example, b.example"), ""},
		{"empty client names refuse to start", append(slices.Clone(files), "--client-names= "), "--client-names is required"},
		{"no client names", files, "--client-names is required"},
		{"no server certificate", []string{"--client-names=a", "--tls-key-file=k", "--client-ca-file=ca"}, "--tls-cert-file is required"},
		{"an upstream not in http", append(slices.Clone(files), "--client-names=a", "--upstream=https://x"), "not an http URL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, err := parse(tc.args)
			switch {
			case tc.want == "" && err != nil:
				t.Fatal(err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("error %v, want %q", err, tc.want)
			case tc.want == "" && !slices.Equal(o.cfg.ClientNames, []string{"a.example", "b.example"}):
				t.Errorf("names %v", o.cfg.ClientNames)
			}
		})
	}
}
