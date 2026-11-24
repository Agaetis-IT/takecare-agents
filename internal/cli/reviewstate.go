// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

const (
	reviewStateVersion = 1
	reviewStateFile    = "review.yaml"
)

// Decisions, as .takecare/review.yaml records them.
const (
	decidedAccepted  = "accepted"
	decidedValidated = "validated"
	decidedSkipped   = "skipped"
)

// reviewState is .takecare/review.yaml (spec 13 §5): per survey, the
// proposal ids taken or skipped, with a digest of each proposal and the
// Map revision. It holds no entry content.
type reviewState struct {
	Version int              `json:"version"`
	Surveys []surveyDecision `json:"surveys"`
}

type surveyDecision struct {
	Survey      string     `json:"survey"`
	MapRevision string     `json:"mapRevision,omitempty"`
	Proposals   []decision `json:"proposals"`
}

type decision struct {
	ID       string `json:"id"`
	Decision string `json:"decision"`
	Digest   string `json:"digest"`
}

// digest identifies a proposal's content: the same id with another entry
// is a new decision to make.
func digest(p *v1alpha1.Proposal) string {
	sum := sha256.Sum256([]byte(string(p.Action) + "\x00" + string(p.Key) + "\x00" + p.Entry))
	return hex.EncodeToString(sum[:])[:16]
}

func loadReviewState(dir string) (*reviewState, error) {
	raw, err := os.ReadFile(filepath.Join(dir, reviewStateFile)) //nolint:gosec // the review record the person points at
	if errors.Is(err, fs.ErrNotExist) {
		return &reviewState{Version: reviewStateVersion}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the review record: %w", err)
	}
	var s reviewState
	if err := yaml.UnmarshalStrict(raw, &s); err != nil {
		return nil, fmt.Errorf("read the review record %s: %w", filepath.Join(dir, reviewStateFile), err)
	}
	if s.Version != reviewStateVersion {
		return nil, fmt.Errorf("the review record has version %d, this takecare reads %d", s.Version, reviewStateVersion)
	}
	return &s, nil
}

// decided reports whether p was decided, in any survey, with the same
// content; the latest decision on its id counts.
func (s *reviewState) decided(p *v1alpha1.Proposal) bool {
	found := ""
	for _, sv := range s.Surveys {
		for _, d := range sv.Proposals {
			if d.ID == p.ID {
				found = d.Digest
			}
		}
	}
	return found != "" && found == digest(p)
}

// record adds the decisions of one run, replacing earlier decisions on
// the same ids for that survey.
func (s *reviewState) record(survey, revision string, ds []decision) {
	i := slices.IndexFunc(s.Surveys, func(sv surveyDecision) bool { return sv.Survey == survey })
	if i < 0 {
		s.Surveys = append(s.Surveys, surveyDecision{Survey: survey})
		i = len(s.Surveys) - 1
	}
	sv := &s.Surveys[i]
	sv.MapRevision = revision
	for _, d := range ds {
		sv.Proposals = slices.DeleteFunc(sv.Proposals, func(o decision) bool { return o.ID == d.ID })
		sv.Proposals = append(sv.Proposals, d)
	}
	slices.SortFunc(sv.Proposals, func(a, b decision) int { return strings.Compare(a.ID, b.ID) })
}

// git runs the user's own git in dir, so that their identity, signing and
// hooks apply (spec 13 §7), and returns its output.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec // the user's own git, with review's fixed verbs
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return strings.TrimRight(string(out), "\n"), nil // porcelain lines start with a space
}

// dirty returns the paths among paths that the working tree holds changes
// to, staged or not, untracked included.
func dirty(ctx context.Context, dir string, paths []string) ([]string, error) {
	out, err := git(ctx, dir, append([]string{"status", "--porcelain", "--untracked-files=all", "--"}, paths...)...)
	if err != nil || out == "" {
		return nil, err
	}
	var d []string
	for line := range strings.SplitSeq(out, "\n") {
		if len(line) > 3 {
			d = append(d, line[3:])
		}
	}
	return d, nil
}
