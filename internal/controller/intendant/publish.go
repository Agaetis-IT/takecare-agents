// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package intendant

import (
	"context"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/yaml"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
	"github.com/agaetis-it/takecare-agents/internal/report"
)

// publishTimeout bounds one digest's publication: report.Post's four
// attempts of at most 30 s each (the MCP client's timeout) and its 2+4+8 s
// of waits between them, 134 s, within 3 minutes.
const publishTimeout = 3 * time.Minute

// Why a digest is not posted, in status.published.reason.
const (
	reasonNothing    = "status only: no proposal to review"
	reasonApprentice = "status only: the Apprentice level has no review destination"
	reasonNoDest     = "status only: the Intendant's level has no report destination"
	reasonNoReport   = "the Intendant has no report connector"
	reasonGone       = "status only: the Intendant no longer exists"
)

// destination is where an Intendant's digest goes (spec 10 §6), the
// Doc's rule: the Apprentice's review destination when one is set, the
// Practitioner's report destination otherwise.
func destination(in *v1alpha1.TakeCareIntendant) (string, string) {
	if in.Spec.Level == v1alpha1.LevelApprentice || in.Spec.Level == "" {
		if in.Spec.Report.Apprentice == "" {
			return "", reasonApprentice
		}
		return in.Spec.Report.Apprentice, ""
	}
	if in.Spec.Report.Practitioner == "" {
		return "", reasonNoDest
	}
	return in.Spec.Report.Practitioner, ""
}

// startPublish publishes a completed survey's digest in a goroutine of its
// own, so that its retries hold neither the Intendant's next survey nor
// the queue.
func (w *Runner) startPublish(ctx context.Context, key types.NamespacedName) {
	if w.Poster == nil {
		return
	}
	w.wg.Go(func() { w.publish(ctx, key) })
}

// publish posts the digest of a Completed survey not yet published through
// its Intendant's report connector, and records the outcome in
// status.published; a final failure is also a warning event. At least
// once: an operator stopped between the post and the record posts again.
func (w *Runner) publish(parent context.Context, key types.NamespacedName) {
	ctx, cancel := context.WithTimeout(parent, publishTimeout)
	defer cancel()
	logger := log.FromContext(ctx).WithValues("survey", key.Name)
	var s v1alpha1.TakeCareSurvey
	if err := w.Reader.Get(ctx, key, &s); err != nil {
		if !apierrors.IsNotFound(err) {
			logger.Error(err, "read the survey to publish")
		}
		return
	}
	if s.Status.Phase != v1alpha1.RunCompleted || s.Status.Published != nil {
		return
	}
	result := w.post(ctx, &s)
	if parent.Err() != nil {
		return // the operator stops: the next leader publishes
	}
	if err := w.patch(parent, &s, func(st *v1alpha1.TakeCareSurveyStatus) { st.Published = &result }); err != nil {
		logger.Error(err, "record the publication")
	}
	if !result.OK && w.Recorder != nil {
		w.Recorder.Eventf(&s, nil, corev1.EventTypeWarning, "PublicationFailed", "Publish", "%s", result.Reason)
	}
}

func (w *Runner) post(ctx context.Context, s *v1alpha1.TakeCareSurvey) v1alpha1.Publication {
	if c := s.Status.ProposalCounts; c == nil || c.Total == 0 {
		return v1alpha1.Publication{OK: true, Reason: reasonNothing}
	}
	var in v1alpha1.TakeCareIntendant
	if err := w.Reader.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: s.Spec.IntendantRef.Name}, &in); err != nil {
		if apierrors.IsNotFound(err) {
			return v1alpha1.Publication{OK: true, Reason: reasonGone}
		}
		return v1alpha1.Publication{Reason: "read the Intendant: " + err.Error()}
	}
	dest, why := destination(&in)
	if dest == "" {
		return v1alpha1.Publication{OK: true, Reason: why}
	}
	result := v1alpha1.Publication{Connector: in.Spec.Connectors.Report, Destination: dest}
	if in.Spec.Connectors.Report == "" {
		result.Reason = reasonNoReport
		return result
	}
	d := NewDigest(s, w.allProposals(ctx, s))
	after := w.After
	if after == nil {
		after = time.After
	}
	if err := report.Post(ctx, w.Client, w.Poster, after, w.Backoff, s.Namespace, in.Spec.Connectors.Report, dest, d.Message()); err != nil {
		result.Reason = err.Error()
		return result
	}
	result.OK = true
	return result
}

// allProposals reads the survey's full set from its ConfigMap; the
// status's first proposals when it cannot be read.
func (w *Runner) allProposals(ctx context.Context, s *v1alpha1.TakeCareSurvey) []v1alpha1.Proposal {
	var cm corev1.ConfigMap
	if s.Status.ProposalsConfigMap == "" ||
		w.Reader.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: s.Status.ProposalsConfigMap}, &cm) != nil {
		return s.Status.Proposals
	}
	out := make([]v1alpha1.Proposal, 0, len(cm.Data))
	for _, v := range cm.Data {
		var p v1alpha1.Proposal
		if yaml.Unmarshal([]byte(v), &p) == nil {
			out = append(out, p)
		}
	}
	return out
}

// resumePublish publishes what a stopped operator left Completed and not
// yet published: in one goroutine, one survey after another, oldest
// first, so that a long stop gives neither a fan-out of goroutines nor a
// burst of digests at once.
func (w *Runner) resumePublish(ctx context.Context) {
	if w.Poster == nil {
		return
	}
	var list v1alpha1.TakeCareSurveyList
	if err := w.Reader.List(ctx, &list); err != nil {
		log.FromContext(ctx).Error(err, "list the surveys to publish")
		return
	}
	var pending []v1alpha1.TakeCareSurvey
	for _, s := range list.Items {
		if s.Status.Phase == v1alpha1.RunCompleted && s.Status.Published == nil {
			pending = append(pending, s)
		}
	}
	if len(pending) == 0 {
		return
	}
	slices.SortStableFunc(pending, func(a, b v1alpha1.TakeCareSurvey) int {
		if c := a.CreationTimestamp.Compare(b.CreationTimestamp.Time); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	w.wg.Go(func() {
		for i := range pending {
			if ctx.Err() != nil {
				return
			}
			w.publish(ctx, client.ObjectKeyFromObject(&pending[i]))
		}
	})
}
