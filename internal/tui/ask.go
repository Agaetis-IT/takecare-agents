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
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"

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

// Screen is where the forms run: the person's input and output, whether
// line prompts replace the full screen, and whether color is off.
type Screen struct {
	In         io.Reader
	Out        io.Writer
	Accessible bool
	NoColor    bool
	// options are Bubble Tea's options for the full screen: a test gives
	// the window size a terminal would.
	options []tea.ProgramOption
}

// Ask asks q and returns the answer, checked with CheckAnswer: an answer
// shaped like a credential is refused at input, without echoing it. A
// question's default is prefilled, never answered in the person's place:
// they confirm it, change it, or say they don't know.
func (s Screen) Ask(ctx context.Context, q *questionnaire.Question) (questionnaire.Answer, error) {
	if q.Kind == questionnaire.KindSecretRef {
		return s.askSecretRef(ctx, q)
	}
	var (
		input string
		field huh.Field
	)
	def, hasDef := defaultInput(q)
	hint := "Type " + DontKnow + " if you don't know."
	// In line prompts an empty line keeps the default, and None stands for
	// an empty answer when the question accepts one.
	none := false
	if hasDef && s.Accessible {
		_, err := Parse(q, "")
		none = err == nil
	}
	validate := func(in string) error {
		if hasDef && s.Accessible && strings.TrimSpace(in) == "" || none && strings.TrimSpace(in) == None {
			return nil
		}
		_, err := Parse(q, in)
		return err
	}
	input = def
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
	default:
		field = huh.NewInput().Title(">").Value(&input).Validate(validate)
	}
	if hasDef {
		hint = defaultHint(def, s.Accessible, none) + "\n" + hint
	}
	note := huh.NewNote().Title(q.Text).Description("Why: " + q.Why + "\n" + hint)
	if err := s.run(ctx, huh.NewGroup(note, field)); err != nil {
		return questionnaire.Answer{}, fmt.Errorf("%s: %w", q.ID, err)
	}
	if none && strings.TrimSpace(input) == None {
		input = ""
	}
	return Parse(q, input)
}

// None is what a person types in line prompts for an empty answer to a
// question whose default an empty line would keep.
const None = "-"

// defaultInput is q's default as the form shows it, and false without one.
func defaultInput(q *questionnaire.Question) (string, bool) {
	a, ok := q.DefaultAnswer()
	if !ok {
		return "", false
	}
	switch v := a.Value.(type) {
	case string:
		return v, true
	case []string:
		return strings.Join(v, ", "), true
	case bool:
		if v {
			return yes, true
		}
		return no, true
	}
	return "", false
}

// defaultHint tells the person the default is theirs to confirm.
func defaultHint(def string, accessible, none bool) string {
	switch {
	case !accessible:
		return "Prefilled with the usual answer: " + def + ". Confirm it or change it."
	case none:
		return "The usual answer is " + def + ". Press Enter to keep it, type another answer, or " + None + " for none."
	}
	return "The usual answer is " + def + ". Press Enter to keep it, or type another answer."
}

// The labels of the three fields of a secretRef question.
const (
	labelNamespace = "Secret namespace"
	labelName      = "Secret name"
	labelKey       = "Key in the Secret"
)

// askSecretRef asks where a Secret lives in three fields: its namespace,
// its name and the key in it. DontKnow in the namespace is "I don't
// know"; the name and the key may then stay empty.
func (s Screen) askSecretRef(ctx context.Context, q *questionnaire.Question) (questionnaire.Answer, error) {
	var ns, name, key string
	unknown := func() bool { return strings.TrimSpace(ns) == DontKnow }
	// Each field is checked alone, the other parts standing in valid.
	part := func(r questionnaire.SecretRef) error {
		return questionnaire.CheckAnswer(q, questionnaire.Answer{Value: r})
	}
	const stand = "x"
	note := huh.NewNote().Title(q.Text).Description("Why: " + q.Why + "\n" +
		"Name the Secret, never write its value. Type " + DontKnow + " as the namespace if you don't know.")
	fields := []huh.Field{
		note,
		huh.NewInput().Title(labelNamespace).Value(&ns).Validate(func(in string) error {
			if strings.TrimSpace(in) == DontKnow {
				return nil
			}
			return part(questionnaire.SecretRef{Namespace: strings.TrimSpace(in), Name: stand})
		}),
		huh.NewInput().Title(labelName).Value(&name).Validate(func(in string) error {
			if unknown() && (strings.TrimSpace(in) == "" || strings.TrimSpace(in) == DontKnow) {
				return nil
			}
			return part(questionnaire.SecretRef{Namespace: stand, Name: strings.TrimSpace(in)})
		}),
		huh.NewInput().Title(labelKey).Value(&key).Validate(func(in string) error {
			if unknown() && strings.TrimSpace(in) == DontKnow {
				return nil
			}
			return part(questionnaire.SecretRef{Namespace: stand, Name: stand, Key: strings.TrimSpace(in)})
		}),
	}
	if err := s.run(ctx, huh.NewGroup(fields...)); err != nil {
		return questionnaire.Answer{}, fmt.Errorf("%s: %w", q.ID, err)
	}
	return ParseSecretRef(q, ns, name, key)
}

// ParseSecretRef turns the three fields of a secretRef question into an
// answer, checked with CheckAnswer. DontKnow as the namespace is "I don't
// know".
func ParseSecretRef(q *questionnaire.Question, namespace, name, key string) (questionnaire.Answer, error) {
	if strings.TrimSpace(namespace) == DontKnow {
		return questionnaire.Answer{Unknown: true}, nil
	}
	a := questionnaire.Answer{Value: questionnaire.SecretRef{
		Namespace: strings.TrimSpace(namespace), Name: strings.TrimSpace(name), Key: strings.TrimSpace(key)}}
	if err := questionnaire.CheckAnswer(q, a); err != nil {
		return questionnaire.Answer{}, err
	}
	return a, nil
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
	f := huh.NewForm(g).WithAccessible(s.Accessible).WithShowHelp(!s.Accessible)
	opts := slices.Clone(s.options)
	if s.NoColor {
		// The full screen goes through Bubble Tea, which writes for the
		// color profile it is given: ASCII keeps text attributes and drops
		// colors, as NO_COLOR does. Line prompts are written by Huh as the
		// theme renders them: the theme holds no color.
		f = f.WithTheme(huh.ThemeFunc(noColorTheme))
		opts = append(opts, tea.WithColorProfile(colorprofile.ASCII))
	}
	// WithProgramOptions replaces the options set before it, input and
	// output included: it comes first.
	f = f.WithProgramOptions(opts...).WithInput(in).WithOutput(s.Out)
	if err := f.RunWithContext(ctx); err != nil {
		return err
	}
	if in.ended {
		return ErrInputEnded
	}
	return nil
}

// noColorTheme is Huh's base theme, the one its colored themes build on,
// without its few colors: the buttons and the placeholder. The focused
// button is shown reversed, so that a confirmation still shows which
// button is chosen. Its help keys keep their colors: line prompts show no
// help, and the full screen drops colors by its profile.
func noColorTheme(isDark bool) *huh.Styles {
	t := huh.ThemeBase(isDark)
	button := lipgloss.NewStyle().Padding(0, 2).MarginRight(1)
	for _, f := range []*huh.FieldStyles{&t.Focused, &t.Blurred} {
		f.FocusedButton = button.Reverse(true)
		f.BlurredButton = button
		f.TextInput.Placeholder = lipgloss.NewStyle()
	}
	return t
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
// Secret reference is asked in three fields (ParseSecretRef).
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
		return questionnaire.Answer{}, fmt.Errorf("%s: a Secret is named in three fields", q.ID)
	}
	a := questionnaire.Answer{Value: v}
	if err := questionnaire.CheckAnswer(q, a); err != nil {
		return questionnaire.Answer{}, err
	}
	return a, nil
}
