// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package scenarios holds the JSON Schema of a scenario file (spec 14 §2),
// embedded for the player (test/scenarios/player). The scenario files sit
// beside it, one <id>.yaml per scenario.
package scenarios

import _ "embed"

// Schema is schema.json.
//
//go:embed schema.json
var Schema []byte
