// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package questionnaire

import (
	"bytes"
	"fmt"
	"maps"
	"slices"

	"go.yaml.in/yaml/v3"
)

// AnswersVersion is the version of the answers file format.
const AnswersVersion = 1

// AnswersFile is .takecare/answers.yaml (spec 13 §5), and the file
// takecare init --answers reads: the answers keyed by id, the ids answered
// "I don't know", and the version of the question set. It holds no secret
// value: a secretRef answer is a Secret's namespace, name and key.
type AnswersFile struct {
	Version     int `yaml:"version"`
	QuestionSet int `yaml:"questionSet,omitempty"`
	// Created is the date of the first run, which the Map entries it
	// wrote carry, so that a later run renders them again identically.
	Created string         `yaml:"created,omitempty"`
	Answers map[string]any `yaml:"answers"`
	Unknown []string       `yaml:"unknown,omitempty"`
	// When records, for each answered question that has a when, the when
	// it was asked under: a question whose when changed is asked again.
	When map[string]string `yaml:"when,omitempty"`
}

// WhenOf is q's when as AnswersFile.When records it, empty without one.
func WhenOf(q *Question) string {
	switch {
	case q.When == nil:
		return ""
	case q.When.Equals == nil:
		return q.When.ID
	}
	return q.When.ID + "=" + *q.When.Equals
}

// DecodeAnswers reads an answers file strictly. Answers to the questions
// of set are typed by their kind and checked (CheckAnswer); answers to ids
// set does not hold, from question files not given this time, stay in the
// file, and EncodeAnswers keeps them.
func DecodeAnswers(set *Set, raw []byte) (*AnswersFile, map[string]Answer, error) {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	var f AnswersFile
	if err := dec.Decode(&f); err != nil {
		return nil, nil, fmt.Errorf("not an answers file: %w", err)
	}
	if f.Version != AnswersVersion {
		return nil, nil, fmt.Errorf("answers file version %d, want %d", f.Version, AnswersVersion)
	}
	out := map[string]Answer{}
	for _, id := range f.Unknown {
		out[id] = Answer{Unknown: true}
	}
	for _, id := range slices.Sorted(maps.Keys(f.Answers)) {
		q := set.Question(id)
		if q == nil {
			continue
		}
		if _, dup := out[id]; dup {
			return nil, nil, fmt.Errorf("%s: both answered and unknown", id)
		}
		v, err := typed(q, f.Answers[id])
		if err != nil {
			return nil, nil, err
		}
		a := Answer{Value: v}
		if err := CheckAnswer(q, a); err != nil {
			return nil, nil, err
		}
		out[id] = a
	}
	return &f, out, nil
}

// typed turns a decoded YAML value into the type q's kind takes.
func typed(q *Question, v any) (any, error) {
	bad := fmt.Errorf("%s: not a %s answer", q.ID, q.Kind)
	switch q.Kind {
	case KindText, KindChoice:
		if v == nil {
			return "", nil
		}
		s, ok := v.(string)
		if !ok {
			return nil, bad
		}
		return s, nil
	case KindBool:
		b, ok := v.(bool)
		if !ok {
			return nil, bad
		}
		return b, nil
	case KindList:
		l, ok := v.([]any)
		if !ok && v != nil {
			return nil, bad
		}
		out := make([]string, 0, len(l))
		for _, x := range l {
			s, ok := x.(string)
			if !ok {
				return nil, bad
			}
			out = append(out, s)
		}
		return out, nil
	case KindSecretRef:
		m, ok := v.(map[string]any)
		if !ok {
			return nil, bad
		}
		var r SecretRef
		for k, x := range m {
			s, ok := x.(string)
			switch {
			case !ok:
				return nil, bad
			case k == "namespace":
				r.Namespace = s
			case k == "name":
				r.Name = s
			case k == "key":
				r.Key = s
			default:
				return nil, fmt.Errorf("%s: unknown field %q", q.ID, k)
			}
		}
		return r, nil
	}
	return nil, bad
}

// EncodeAnswers writes answers into f, keeping the answers f holds for
// ids set does not know, and returns the file's bytes.
func EncodeAnswers(set *Set, f *AnswersFile, answers map[string]Answer) ([]byte, error) {
	next := map[string]any{}
	for id, v := range f.Answers {
		if set.Question(id) == nil {
			next[id] = v
		}
	}
	f.Unknown = slices.DeleteFunc(f.Unknown, func(id string) bool { return set.Question(id) != nil })
	f.When = map[string]string{}
	for _, q := range set.Questions {
		a, ok := answers[q.ID]
		if !ok {
			continue
		}
		if w := WhenOf(q); w != "" {
			f.When[q.ID] = w
		}
		if a.Unknown {
			f.Unknown = append(f.Unknown, q.ID)
			continue
		}
		next[q.ID] = a.Value
	}
	slices.Sort(f.Unknown)
	f.Version, f.Answers = AnswersVersion, next
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(f); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
