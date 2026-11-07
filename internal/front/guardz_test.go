// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package front

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

const listShop = "shop/pods//list"

const frontSA = "system:serviceaccount:takecare-cluster:takecare-cluster"

// fakeAPI answers SelfSubjectReviews with the front's ServiceAccount and
// SelfSubjectAccessReviews from a set of "namespace/resource/subresource/verb".
func fakeAPI(allowed *atomic.Value, reviews *atomic.Int32) *fake.Clientset {
	c := fake.NewClientset()
	c.PrependReactor("create", "selfsubjectreviews", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, &authenticationv1.SelfSubjectReview{Status: authenticationv1.SelfSubjectReviewStatus{UserInfo: authenticationv1.UserInfo{Username: frontSA}}}, nil
	})
	c.PrependReactor("create", "selfsubjectaccessreviews", func(a k8stesting.Action) (bool, runtime.Object, error) {
		reviews.Add(1)
		review := a.(k8stesting.CreateAction).GetObject().(*authorizationv1.SelfSubjectAccessReview)
		ra := review.Spec.ResourceAttributes
		set, _ := allowed.Load().(map[string]bool)
		review.Status.Allowed = set[ra.Namespace+"/"+ra.Resource+"/"+ra.Subresource+"/"+ra.Verb]
		return true, review, nil
	})
	return c
}

// The verdict is answered 200 whatever it says; 503 only without a
// verdict.
func TestGuardz(t *testing.T) {
	var allowed atomic.Value
	var reviews atomic.Int32
	allowed.Store(map[string]bool{listShop: true, "shop/secrets//get": true})
	now := time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)
	g := &Guard{Client: fakeAPI(&allowed, &reviews), Namespaces: []string{"shop", "billing"}, Interval: 10 * time.Minute,
		Logger: slog.New(slog.DiscardHandler), Now: func() time.Time { return now }}
	read := func() (int, guardz) {
		t.Helper()
		rec := httptest.NewRecorder()
		g.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/guardz", nil))
		var body guardz
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return rec.Code, body
	}

	code, body := read()
	if code != http.StatusOK || body.ServiceAccount != frontSA ||
		!cmp.Equal(body.Violations, []string{"read Secrets in shop"}) || !cmp.Equal(body.Missing, []string{"list pods in billing"}) {
		t.Fatalf("first check: %d %+v", code, body)
	}
	// Within the interval: the last verdict, no new review.
	asked := reviews.Load()
	allowed.Store(map[string]bool{listShop: true, "billing/pods//list": true})
	if _, again := read(); reviews.Load() != asked || len(again.Violations) != 1 {
		t.Fatalf("checked again within the interval: %d reviews, %+v", reviews.Load()-asked, again)
	}
	// After the interval: checked again, clean.
	now = now.Add(10 * time.Minute)
	code, body = read()
	if code != http.StatusOK || len(body.Violations) != 0 || len(body.Missing) != 0 || !body.CheckedAt.Equal(now) {
		t.Fatalf("after the interval: %d %+v", code, body)
	}
	// The API refuses the reviews: no verdict, 503 with the error.
	refusing := fake.NewClientset()
	refusing.PrependReactor("create", "*", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("the API server is unreachable")
	})
	g.Client, now = refusing, now.Add(10*time.Minute)
	if code, body = read(); code != http.StatusServiceUnavailable || body.Error == "" {
		t.Errorf("without a verdict: %d %+v", code, body)
	}
	// After an error, the next request a minute later asks again.
	g.Client, now = fakeAPI(&allowed, &reviews), now.Add(time.Minute)
	if code, body = read(); code != http.StatusOK || body.Error != "" {
		t.Errorf("a minute after an error: %d %+v", code, body)
	}
}

// /guardz is on the mTLS endpoint, for an accepted client, never on the
// probe port.
func TestGuardzOnTheMTLSEndpointOnly(t *testing.T) {
	var allowed atomic.Value
	var reviews atomic.Int32
	allowed.Store(map[string]bool{listShop: true})
	f := startWith(t, &Guard{Client: fakeAPI(&allowed, &reviews), Namespaces: []string{"shop"}, Interval: time.Minute, Logger: slog.New(slog.DiscardHandler)})

	good := f.ca.client(t, "admin", adminName)
	if code, body, _, err := f.get(t, "/guardz", &good); err != nil || code != http.StatusOK {
		t.Fatalf("an accepted client: %d %s %v", code, body, err)
	}
	stranger := f.ca.client(t, "stranger", "stranger.example")
	if code, _, _, err := f.get(t, "/guardz", &stranger); err != nil || code != http.StatusForbidden {
		t.Fatalf("a name not accepted: %d %v", code, err)
	}
	rec := httptest.NewRecorder()
	f.front.ProbeHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/guardz", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("the probe port serves /guardz: %d", rec.Code)
	}
}
