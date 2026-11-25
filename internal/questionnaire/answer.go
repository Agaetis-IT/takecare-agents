// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package questionnaire

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// SecretRef names where a Secret lives, never its value.
type SecretRef struct {
	Namespace string `json:"namespace" yaml:"namespace"`
	Name      string `json:"name" yaml:"name"`
	Key       string `json:"key,omitempty" yaml:"key,omitempty"`
}

// Answer is one question's answer: Unknown ("I don't know"), or Value of
// the question's kind: a string for text and choice, a []string for list,
// a bool, a SecretRef.
type Answer struct {
	Unknown bool
	Value   any
}

// CheckAnswer checks a against q: its type, its choice or pattern, and no
// string in it shaped like a credential. Its errors never hold the answer.
func CheckAnswer(q *Question, a Answer) error {
	if a.Unknown {
		return nil
	}
	var texts []string
	switch v := a.Value.(type) {
	case string:
		if q.Kind != KindText && q.Kind != KindChoice {
			return fmt.Errorf("%s: a text answer to a %s question", q.ID, q.Kind)
		}
		if q.Kind == KindChoice && !slices.Contains(q.Choices, v) {
			return fmt.Errorf("%s: not one of the choices %v", q.ID, q.Choices)
		}
		texts = []string{v}
	case []string:
		if q.Kind != KindList {
			return fmt.Errorf("%s: a list answer to a %s question", q.ID, q.Kind)
		}
		texts = v
	case bool:
		if q.Kind != KindBool {
			return fmt.Errorf("%s: a yes or no answer to a %s question", q.ID, q.Kind)
		}
	case SecretRef:
		if q.Kind != KindSecretRef {
			return fmt.Errorf("%s: a Secret reference answer to a %s question", q.ID, q.Kind)
		}
		if v.Namespace == "" || v.Name == "" {
			return fmt.Errorf("%s: a Secret reference needs a namespace and a name", q.ID)
		}
		texts = []string{v.Namespace, v.Name, v.Key}
	default:
		return fmt.Errorf("%s: an answer of type %T", q.ID, a.Value)
	}
	for _, t := range texts {
		if err := CheckCredential(t); err != nil {
			return fmt.Errorf("%s: %w", q.ID, err)
		}
	}
	if q.pattern != nil && (q.Kind == KindText || q.Kind == KindList) {
		for _, t := range texts {
			if !q.pattern.MatchString(t) {
				return fmt.Errorf("%s: does not match %s", q.ID, q.Validate.Pattern)
			}
		}
	}
	return nil
}

// Shown tells whether q is asked given the answers so far: it has no when,
// or its when target is shown and answered, with the value equals gives
// when it gives one.
func (s *Set) Shown(q *Question, answers map[string]Answer) bool {
	for at := q; at.When != nil; at = s.byID[at.When.ID] {
		a, ok := answers[at.When.ID]
		if !ok || a.Unknown || empty(templateValue(a.Value)) {
			return false
		}
		if at.When.Equals != nil && stringOf(a.Value) != *at.When.Equals {
			return false
		}
	}
	return true
}

func stringOf(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	}
	return ""
}

// Data is what an output's template sees: every question's answer, keyed
// by id, an id a.b being read as {{ .a.b }}. An unknown answer, and one to
// a question not shown, is its kind's zero value, so that a missing key in
// a template is always a typo. The data hold strings, lists of strings,
// booleans and maps of them, never a function.
func (s *Set) Data(answers map[string]Answer) (map[string]any, error) {
	data := map[string]any{}
	for _, q := range s.Questions {
		v := zero(q.Kind)
		if a, ok := answers[q.ID]; ok && !a.Unknown && s.Shown(q, answers) {
			if err := CheckAnswer(q, a); err != nil {
				return nil, err
			}
			v = templateValue(a.Value)
		}
		if err := put(data, q.ID, v); err != nil {
			return nil, err
		}
	}
	return data, nil
}

func zero(k Kind) any {
	switch k {
	case KindList:
		return []string{}
	case KindBool:
		return false
	case KindSecretRef:
		return templateValue(SecretRef{})
	}
	return ""
}

// templateValue turns a SecretRef into a map a template reads as
// {{ .x.namespace }}; other values are already strings, lists or booleans.
func templateValue(v any) any {
	if r, ok := v.(SecretRef); ok {
		return map[string]any{"namespace": r.Namespace, "name": r.Name, "key": r.Key}
	}
	return v
}

// put sets data[a][b] = v for id a.b. The loader refused an id under
// another, so no segment is already a value.
func put(data map[string]any, id string, v any) error {
	segs := strings.Split(id, ".")
	for _, s := range segs[:len(segs)-1] {
		next, ok := data[s].(map[string]any)
		if !ok {
			if _, taken := data[s]; taken {
				return errors.New("question " + id + " sits under another question")
			}
			next = map[string]any{}
			data[s] = next
		}
		data = next
	}
	data[segs[len(segs)-1]] = v
	return nil
}
