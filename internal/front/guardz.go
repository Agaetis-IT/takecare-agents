// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package front

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/agaetis-it/takecare-agents/internal/guard"
)

// guardTimeout bounds one run of the check; errorKeep, how long a check
// that failed is served.
const (
	guardTimeout = 10 * time.Second
	errorKeep    = time.Minute
)

// Guard is /guardz (ADR 0016 §4): the read-only guard's check of the pod's
// own ServiceAccount with SelfSubjectAccessReviews, which every
// authenticated subject may create. It serves the last verdict, refreshed
// on the first request and then once per Interval.
type Guard struct {
	Client     kubernetes.Interface
	Namespaces []string
	Interval   time.Duration
	Logger     *slog.Logger
	// Now is replaceable in tests.
	Now func() time.Time

	mu   sync.Mutex
	last *guardz
}

// guardz is /guardz's body.
type guardz struct {
	ServiceAccount string    `json:"serviceAccount"`
	Namespaces     []string  `json:"namespaces"`
	CheckedAt      time.Time `json:"checkedAt"`
	Error          string    `json:"error,omitempty"`
	guard.Verdict
}

func (g *Guard) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

// ServeHTTP answers the verdict with 200, whatever it says: the reader
// (the operator) maps violations and missing namespaces itself. 503 only
// when no verdict could be computed, the API having refused or not
// answered the reviews.
func (g *Guard) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	if g.last == nil || g.now().Sub(g.last.CheckedAt) >= g.keep() {
		g.refresh(r.Context())
	}
	body := *g.last
	g.mu.Unlock()
	code := http.StatusOK
	if body.Error != "" {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, body)
}

// keep is how long the last verdict is served: Interval, or at most a
// minute when it is an error, so that one API blip does not read as an
// unavailable guard for the whole interval. The caller holds mu.
func (g *Guard) keep() time.Duration {
	if g.last.Error != "" {
		return min(g.Interval, errorKeep)
	}
	return g.Interval
}

// refresh runs the check; the caller holds mu.
func (g *Guard) refresh(ctx context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), guardTimeout)
	defer cancel()
	next := &guardz{Namespaces: g.Namespaces, CheckedAt: g.now(), Violations: []string{}, Missing: []string{}}
	who, err := g.Client.AuthenticationV1().SelfSubjectReviews().Create(ctx, &authenticationv1.SelfSubjectReview{}, metav1.CreateOptions{})
	if err == nil {
		next.ServiceAccount = who.Status.UserInfo.Username
		next.Verdict, err = guard.Check(ctx, g.can, g.Namespaces)
	}
	if err != nil {
		next.Error = err.Error()
	}
	if g.last == nil || next.Error != g.last.Error || !slices.Equal(next.Violations, g.last.Violations) || !slices.Equal(next.Missing, g.last.Missing) {
		g.Logger.Info("read-only guard checked", "violations", len(next.Violations), "missing", len(next.Missing), "error", next.Error)
	}
	g.last = next
}

func (g *Guard) can(ctx context.Context, ns, group, resource, subresource, verb string) (bool, error) {
	review, err := g.Client.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authorizationv1.SelfSubjectAccessReview{
		Spec: authorizationv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &authorizationv1.ResourceAttributes{
			Namespace: ns, Group: group, Resource: resource, Subresource: subresource, Verb: verb,
		}},
	}, metav1.CreateOptions{})
	if err != nil {
		return false, err
	}
	return review.Status.Allowed, nil
}
