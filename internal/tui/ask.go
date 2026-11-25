// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package tui holds the screens of the takecare command (spec 13 §8):
// Huh forms, full screen on a terminal, or line prompts for screen readers
// in Huh's accessible mode.
package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"charm.land/huh/v2"

	"github.com/agaetis-it/takecare-agents/internal/questionnaire"
)

// DontKnow is what a person types, or picks, for "I don't know".
const DontKnow = "?"

const dontKnowLabel = "I don't know"

// The answers to a bool question.
const (
	yes = "yes"
	no  = "no"
)

// ErrInputEnded is returned when the input ends before an answer.
var ErrInputEnded = errors.New("the input ended before an answer")

// Screen is where the forms run: the person's input and output, and
// whether line prompts replace the full screen.
type Screen struct {
	In         io.Reader
	Out        io.Writer
	Accessible bool
}

// Ask asks q and returns the answer, checked with CheckAnswer: an answer
// shaped like a credential is refused at input, without echoing it.
func (s Screen) Ask(ctx context.Context, q *questionnaire.Question) (questionnaire.Answer, error) {
	var (
		input string
		field huh.Field
	)
	hint := "Type " + DontKnow + " if you don't know."
	validate := func(in string) error {
		_, err := Parse(q, in)
		return err
	}
	switch q.Kind {
	case questionnaire.KindChoice, questionnaire.KindBool:
		values := q.Choices
		if q.Kind == questionnaire.KindBool {
			values = []string{yes, no}
		}
		opts := make([]huh.Option[string], 0, len(values)+1)
		for _, c := range values {
			opts = append(opts, huh.NewOption(c, c))
		}
		opts = append(opts, huh.NewOption(dontKnowLabel, DontKnow))
		field = huh.NewSelect[string]().Title("Answer").Options(opts...).Value(&input)
		hint = "Pick " + dontKnowLabel + " if you don't know."
	case questionnaire.KindList:
		hint = "Separate the items with commas. " + hint
		field = huh.NewInput().Title(">").Value(&input).Validate(validate)
	case questionnaire.KindSecretRef:
		hint = "As <namespace>/<name>, or <namespace>/<name>:<key>; never the value. " + hint
		field = huh.NewInput().Title(">").Value(&input).Validate(validate)
	default:
		field = huh.NewInput().Title(">").Value(&input).Validate(validate)
	}
	note := huh.NewNote().Title(q.Text).Description("Why: " + q.Why + "\n" + hint)
	if err := s.run(ctx, huh.NewGroup(note, field)); err != nil {
		return questionnaire.Answer{}, fmt.Errorf("%s: %w", q.ID, err)
	}
	return Parse(q, input)
}

// Confirm asks a yes or no question.
func (s Screen) Confirm(ctx context.Context, title, description string) (bool, error) {
	var ok bool
	c := huh.NewConfirm().Title(title).Description(description).Value(&ok)
	err := s.run(ctx, huh.NewGroup(c))
	return ok, err
}

// run runs one form. In accessible mode Huh reads lines and cannot tell
// the end of the input from an empty line: the reader says so.
func (s Screen) run(ctx context.Context, g *huh.Group) error {
	in := &watched{r: s.In}
	f := huh.NewForm(g).WithAccessible(s.Accessible).WithInput(in).WithOutput(s.Out).WithShowHelp(!s.Accessible)
	if err := f.RunWithContext(ctx); err != nil {
		return err
	}
	if in.ended {
		return ErrInputEnded
	}
	return nil
}

// watched notes when its reader ends.
type watched struct {
	r     io.Reader
	ended bool
}

func (w *watched) Read(p []byte) (int, error) {
	n, err := w.r.Read(p)
	if errors.Is(err, io.EOF) {
		w.ended = true
	}
	return n, err
}

// Parse turns what a person typed for q into an answer, checked with
// CheckAnswer. DontKnow is "I don't know". A list is comma-separated; a
// Secret reference is <namespace>/<name>[:<key>].
func Parse(q *questionnaire.Question, in string) (questionnaire.Answer, error) {
	in = strings.TrimSpace(in)
	if in == DontKnow {
		return questionnaire.Answer{Unknown: true}, nil
	}
	var v any = in
	switch q.Kind {
	case questionnaire.KindBool:
		v = in == yes
		if in != yes && in != no {
			return questionnaire.Answer{}, fmt.Errorf("%s: answer yes or no", q.ID)
		}
	case questionnaire.KindList:
		items := []string{}
		for it := range strings.SplitSeq(in, ",") {
			if it = strings.TrimSpace(it); it != "" {
				items = append(items, it)
			}
		}
		v = items
	case questionnaire.KindSecretRef:
		ref, key, _ := strings.Cut(in, ":")
		ns, name, ok := strings.Cut(ref, "/")
		if !ok {
			return questionnaire.Answer{}, fmt.Errorf("%s: write <namespace>/<name>[:<key>]", q.ID)
		}
		v = questionnaire.SecretRef{Namespace: ns, Name: name, Key: key}
	}
	a := questionnaire.Answer{Value: v}
	if err := questionnaire.CheckAnswer(q, a); err != nil {
		return questionnaire.Answer{}, err
	}
	return a, nil
}
