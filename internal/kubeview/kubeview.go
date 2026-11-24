// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package kubeview is the takecare command's view of a cluster: get and
// list, nothing else (spec 13 §6). No other verb is reachable from it.
package kubeview

import (
	"context"
	"fmt"
	"net/http"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

// requestTimeout bounds every request to the API server.
const requestTimeout = 30 * time.Second

// Reader reads a cluster with get and list only.
type Reader struct {
	r client.Reader
}

// Load reads the kubeconfig with client-go's loading rules: the explicit
// path, then KUBECONFIG, then the default file; contextName, when set,
// overrides the current context for this run only. Nothing is written:
// the migration of old kubeconfig paths, which copies a file, is off, and
// no config access is kept, so an auth provider cannot persist into it.
func Load(kubeconfig, contextName string) (*rest.Config, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	rules.ExplicitPath = kubeconfig
	rules.MigrationRules = nil
	merged, err := rules.Load()
	if err != nil {
		return nil, fmt.Errorf("load the kubeconfig: %w", err)
	}
	overrides := &clientcmd.ConfigOverrides{CurrentContext: contextName}
	cfg, err := clientcmd.NewNonInteractiveClientConfig(*merged, contextName, overrides, nil).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load the kubeconfig: %w", err)
	}
	return cfg, nil
}

// New returns a Reader on cfg. Its transport refuses any request but a
// plain GET, a second fence behind the method set.
func New(cfg *rest.Config) (*Reader, error) {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		return nil, err
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		return nil, err
	}
	cfg = rest.CopyConfig(cfg)
	cfg.Timeout = requestTimeout
	cfg.Wrap(getOnly)
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("create the cluster client: %w", err)
	}
	return &Reader{r: c}, nil
}

// Get reads one object.
func (v *Reader) Get(ctx context.Context, key client.ObjectKey, obj client.Object) error {
	return v.r.Get(ctx, key, obj)
}

// List reads a list of objects.
func (v *Reader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	return v.r.List(ctx, list, opts...)
}

// getOnly refuses, before it leaves the process, any request that is not
// a GET, and a GET that would open a watch.
func getOnly(next http.RoundTripper) http.RoundTripper {
	return roundTripper(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet || req.URL.Query().Has("watch") {
			return nil, fmt.Errorf("takecare reads only: %s %s refused", req.Method, req.URL.Path)
		}
		return next.RoundTrip(req)
	})
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
