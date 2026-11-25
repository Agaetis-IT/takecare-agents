// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package crd embeds the CustomResourceDefinitions that make generate
// writes into this directory, so that a tool can check a file against a
// kind's schema offline (spec 13 §4.3).
package crd

import "embed"

// FS holds the takecare.agaetis.fr CRDs, one file per kind.
//
//go:embed takecare.agaetis.fr_*.yaml
var FS embed.FS
