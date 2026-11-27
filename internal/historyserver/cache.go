// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package historyserver

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/agaetis-it/takecare-agents/api/v1alpha1"
)

// NewCache is an informer cache of the investigations, the reviews and the
// maintenances of one namespace, every informer registered: the reader
// refuses any other kind (ReaderFailOnMissingInformer). Its REST mapper is the default one,
// which reads the API discovery that every authenticated account may read
// (the system:discovery ClusterRole): a static mapper would not know the
// list kinds the namespaced cache asks it about. The managed fields are
// dropped and the objects never copied, as the server only reads them
// (controller-runtime v0.25.2, cache.Options). Start it, then wait for its
// sync.
func NewCache(ctx context.Context, cfg *rest.Config, namespace string) (cache.Cache, error) {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("register the API: %w", err)
	}
	c, err := cache.New(cfg, cache.Options{
		Scheme:                       scheme,
		DefaultNamespaces:            map[string]cache.Config{namespace: {}},
		ReaderFailOnMissingInformer:  true,
		DefaultTransform:             cache.TransformStripManagedFields(),
		DefaultUnsafeDisableDeepCopy: new(true),
	})
	if err != nil {
		return nil, fmt.Errorf("build the cache: %w", err)
	}
	for _, obj := range []client.Object{&v1alpha1.TakeCareInvestigation{}, &v1alpha1.TakeCareReview{}, &v1alpha1.TakeCareMaintenance{}} {
		if _, err := c.GetInformer(ctx, obj); err != nil {
			return nil, fmt.Errorf("informer for %T: %w", obj, err)
		}
	}
	return c, nil
}
