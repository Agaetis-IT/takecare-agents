// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

// ConditionMapCurrent says which revision of the Map a map add-on serves.
// It is not part of Ready: a server whose newest revision does not validate
// keeps serving the last valid one.
const ConditionMapCurrent = "MapCurrent"

// Reasons of MapCurrent and its events.
const (
	ReasonInvalidRevision    = "InvalidRevision"
	ReasonStatusUnavailable  = "StatusUnavailable"
	EventMapRevisionInvalid  = "MapRevisionInvalid"
	EventMapRevisionRestored = "MapRevisionRestored"
)

const (
	statuszTimeout = 2 * time.Second
	statuszLimit   = 64 << 10
)

// mapStatus is the part of the Map server's /statusz the operator reads.
type mapStatus struct {
	Revision     string        `json:"revision"`
	RevisionTime *time.Time    `json:"revisionTime"`
	LastError    *statuszError `json:"lastError"`
	FetchError   *statuszError `json:"fetchError"`
}

type statuszError struct {
	Commit string `json:"commit"`
	Error  string `json:"error"`
}

// mapCurrent sets MapCurrent on a map connector: from the add-on's
// /statusz, or NotApplicable for a customer's own server, which is not
// probed.
func (r *Reconciler) mapCurrent(ctx context.Context, c *v1alpha1.TakeCareConnector, st *status) {
	if !slices.Contains(c.Spec.Types, v1alpha1.ConnectorMap) {
		return
	}
	if c.Spec.MCP.Addon == "" {
		st.setStatus(ConditionMapCurrent, metav1.ConditionUnknown, ReasonNotApplicable, "a customer's server is not probed")
		return
	}
	if !meta.IsStatusConditionTrue(st.conditions, v1alpha1.ConditionReachable) {
		st.set(ConditionMapCurrent, false, ReasonStatusUnavailable, "not probed: the server is not reachable")
		return
	}
	ms, err := r.statusz(ctx, AddonURL(c.Spec.MCP.Addon, r.OperatorNamespace, "/statusz"))
	if err == nil {
		st.mapRevision = ms.Revision
	}
	switch {
	case err != nil:
		st.set(ConditionMapCurrent, false, ReasonStatusUnavailable, err.Error())
	case ms.LastError != nil:
		msg := ms.LastError.Error
		if ms.Revision != "" {
			msg += "; serving the last valid revision " + ms.Revision
		}
		st.set(ConditionMapCurrent, false, ReasonInvalidRevision, msg)
	case ms.Revision == "" && ms.FetchError != nil:
		st.set(ConditionMapCurrent, false, ReasonStatusUnavailable, "no revision loaded: "+ms.FetchError.Error)
	case ms.Revision == "":
		st.set(ConditionMapCurrent, false, ReasonStatusUnavailable, "no revision loaded yet")
	default:
		msg := "serving revision " + ms.Revision
		if ms.RevisionTime != nil {
			msg += " of " + ms.RevisionTime.UTC().Format(time.RFC3339)
		}
		if ms.FetchError != nil {
			msg += "; the last fetch failed: " + ms.FetchError.Error
		}
		st.set(ConditionMapCurrent, true, ReasonReady, msg)
	}
}

func (r *Reconciler) statusz(ctx context.Context, url string) (*mapStatus, error) {
	if r.HTTP == nil {
		return nil, errors.New("no HTTP client to read /statusz")
	}
	ctx, cancel := context.WithTimeout(ctx, statuszTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("read /statusz: %w", err)
	}
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("read /statusz: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("read /statusz: HTTP %d", resp.StatusCode)
	}
	var ms mapStatus
	if err := json.NewDecoder(io.LimitReader(resp.Body, statuszLimit)).Decode(&ms); err != nil {
		return nil, fmt.Errorf("read /statusz: not JSON: %w", err)
	}
	return &ms, nil
}

// mapEvent says a transition of MapCurrent once: into an invalid revision,
// or back from one to a current revision.
func (r *Reconciler) mapEvent(c *v1alpha1.TakeCareConnector, before *metav1.Condition, st *status) {
	after := meta.FindStatusCondition(st.conditions, ConditionMapCurrent)
	if after == nil {
		return
	}
	wasInvalid := before != nil && before.Reason == ReasonInvalidRevision
	switch {
	case after.Reason == ReasonInvalidRevision && !wasInvalid:
		r.Recorder.Eventf(c, nil, corev1.EventTypeWarning, EventMapRevisionInvalid, "Reconcile", "%s", after.Message)
	case after.Status == metav1.ConditionTrue && wasInvalid:
		r.Recorder.Eventf(c, nil, corev1.EventTypeNormal, EventMapRevisionRestored, "Reconcile", "%s", after.Message)
	}
}
