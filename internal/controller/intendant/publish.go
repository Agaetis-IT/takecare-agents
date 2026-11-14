// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package intendant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
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
	reasonSame       = "status only: the same proposals as survey "
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
// status.published; a final failure is also a warning event. A digest
// whose proposals are those of the Intendant's last posted one, to the
// same connector and destination, is not posted again. The compare, the
// post and the record hold the Intendant's digest lock, which only the
// publication goroutines take. At least once: an operator stopped between
// the post and the record posts again.
func (w *Runner) publish(parent context.Context, key types.NamespacedName) {
	logger := log.FromContext(parent).WithValues("survey", key.Name)
	var s v1alpha1.TakeCareSurvey
	if err := w.Reader.Get(parent, key, &s); err != nil {
		if !apierrors.IsNotFound(err) {
			logger.Error(err, "read the survey to publish")
		}
		return
	}
	if s.Status.Phase != v1alpha1.RunCompleted || s.Status.Published != nil {
		return
	}
	unlock, ok := w.digestLock(parent, types.NamespacedName{Namespace: s.Namespace, Name: s.Spec.IntendantRef.Name})
	if !ok {
		return // the operator stops: the next leader publishes
	}
	defer unlock()
	// The bound counts from the lock: a digest waiting behind another's
	// retries keeps its whole time.
	ctx, cancel := context.WithTimeout(parent, w.publishTimeout())
	defer cancel()
	if err := w.Reader.Get(ctx, key, &s); err != nil || s.Status.Published != nil {
		return
	}
	result, last := w.post(ctx, &s)
	if parent.Err() != nil {
		return // the operator stops: the next leader publishes
	}
	if last != nil {
		if err := w.recordDigest(parent, &s, last); err != nil {
			logger.Error(err, "record the Intendant's last digest")
		}
	}
	if err := w.patch(parent, &s, func(st *v1alpha1.TakeCareSurveyStatus) { st.Published = &result }); err != nil {
		logger.Error(err, "record the publication")
	}
	if !result.OK && w.Recorder != nil {
		w.Recorder.Eventf(&s, nil, corev1.EventTypeWarning, "PublicationFailed", "Publish", "%s", result.Reason)
	}
}

func (w *Runner) publishTimeout() time.Duration {
	if w.PublishTimeout > 0 {
		return w.PublishTimeout
	}
	return publishTimeout
}

// digestLock takes the Intendant's digest lock, or gives up when ctx
// ends; it returns the unlock.
func (w *Runner) digestLock(ctx context.Context, in types.NamespacedName) (func(), bool) {
	v, _ := w.digestLocks.LoadOrStore(in, make(chan struct{}, 1))
	ch, _ := v.(chan struct{})
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, true
	case <-ctx.Done():
		return nil, false
	}
}

// post posts the survey's digest unless there is nothing to post or it is
// the Intendant's last; it returns the outcome, and what to record as the
// Intendant's last digest when one was posted.
func (w *Runner) post(ctx context.Context, s *v1alpha1.TakeCareSurvey) (v1alpha1.Publication, *v1alpha1.LastDigest) {
	if c := s.Status.ProposalCounts; c == nil || c.Total == 0 {
		return v1alpha1.Publication{OK: true, Reason: reasonNothing}, nil
	}
	var in v1alpha1.TakeCareIntendant
	if err := w.Reader.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: s.Spec.IntendantRef.Name}, &in); err != nil {
		if apierrors.IsNotFound(err) {
			return v1alpha1.Publication{OK: true, Reason: reasonGone}, nil
		}
		return v1alpha1.Publication{Reason: "read the Intendant: " + err.Error()}, nil
	}
	dest, why := destination(&in)
	if dest == "" {
		return v1alpha1.Publication{OK: true, Reason: why}, nil
	}
	result := v1alpha1.Publication{Connector: in.Spec.Connectors.Report, Destination: dest}
	if in.Spec.Connectors.Report == "" {
		result.Reason = reasonNoReport
		return result, nil
	}
	all := w.allProposals(ctx, s)
	this := &v1alpha1.LastDigest{Survey: s.Name, Connector: result.Connector, Destination: dest, Fingerprint: fingerprint(all, s.Status.ProposalCounts.Total)}
	if l := in.Status.LastDigest; l != nil && l.Survey == s.Name {
		// Posted and recorded, the operator stopped before the survey's
		// own record.
		result.Connector, result.Destination, result.OK = l.Connector, l.Destination, true
		return result, nil
	}
	if l := in.Status.LastDigest; l != nil && l.Fingerprint == this.Fingerprint && l.Connector == this.Connector && l.Destination == this.Destination {
		result.OK, result.Reason = true, reasonSame+l.Survey
		return result, nil
	}
	d := NewDigest(s, all)
	after := w.After
	if after == nil {
		after = time.After
	}
	if err := report.Post(ctx, w.Client, w.Poster, after, w.Backoff, s.Namespace, in.Spec.Connectors.Report, dest, d.Message()); err != nil {
		result.Reason = err.Error()
		return result, nil
	}
	result.OK = true
	return result, this
}

// fingerprint is a hash of the proposals' ids, sorted, and their total,
// which counts those a full ConfigMap left out: nothing of an entry.
func fingerprint(all []v1alpha1.Proposal, total int32) string {
	ids := make([]string, 0, len(all))
	for _, p := range all {
		ids = append(ids, p.ID)
	}
	slices.Sort(ids)
	sum := sha256.Sum256([]byte(strings.Join(ids, "\n") + "\n" + strconv.Itoa(int(total))))
	return hex.EncodeToString(sum[:])
}

// recordDigest records the survey's digest as the Intendant's last posted.
func (w *Runner) recordDigest(ctx context.Context, s *v1alpha1.TakeCareSurvey, last *v1alpha1.LastDigest) error {
	var in v1alpha1.TakeCareIntendant
	if err := w.Reader.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: s.Spec.IntendantRef.Name}, &in); err != nil {
		return fmt.Errorf("read the Intendant %s: %w", s.Spec.IntendantRef.Name, err)
	}
	before := in.DeepCopy()
	in.Status.LastDigest = last
	if err := w.Client.Status().Patch(ctx, &in, client.MergeFrom(before)); err != nil {
		return fmt.Errorf("update the Intendant %s: %w", in.Name, err)
	}
	return nil
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
	// Through the cached client, as resume does and for the same reason: the
	// cache is scoped to the operator's namespace, where its Role may list
	// surveys; the API reader would list at the cluster scope.
	var list v1alpha1.TakeCareSurveyList
	if err := w.Client.List(ctx, &list); err != nil {
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
