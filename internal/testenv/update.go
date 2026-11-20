// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package testenv

import (
	"context"

	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Update re-reads obj, applies change and writes it, again on a conflict:
// for a test write on an object the operator writes too, whose status
// write moves the resourceVersion under the test.
func Update[T client.Object](ctx context.Context, c client.Client, obj T, change func(T)) error {
	return write(ctx, c, obj, change, func() error { return c.Update(ctx, obj) })
}

// UpdateStatus is Update for the status subresource.
func UpdateStatus[T client.Object](ctx context.Context, c client.Client, obj T, change func(T)) error {
	return write(ctx, c, obj, change, func() error { return c.Status().Update(ctx, obj) })
}

func write[T client.Object](ctx context.Context, c client.Client, obj T, change func(T), put func() error) error {
	key := client.ObjectKeyFromObject(obj)
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := c.Get(ctx, key, obj); err != nil {
			return err
		}
		change(obj)
		return put()
	})
}
