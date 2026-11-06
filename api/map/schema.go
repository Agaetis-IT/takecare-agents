// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package mapschema holds the JSON Schema of a Map entry (spec 08 §4): the
// file a customer's CI validates its Map with, embedded here for the Map
// server.
package mapschema

import _ "embed"

// Entry is entry.schema.json.
//
//go:embed entry.schema.json
var Entry []byte
