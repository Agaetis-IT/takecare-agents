// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package runs

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"
)

func key(n string) types.NamespacedName {
	return types.NamespacedName{Namespace: "takecare-system", Name: n}
}

func TestQueueIsBoundedAndOldestFirst(t *testing.T) {
	q := NewQueue(2)
	t0 := time.Now()
	if !q.Add(key("b"), t0.Add(time.Second)) || !q.Add(key("a"), t0) {
		t.Fatal("two runs fit in a queue of two")
	}
	if !q.Add(key("a"), t0) {
		t.Error("adding a queued run again is a success")
	}
	if q.Add(key("c"), t0.Add(2*time.Second)) {
		t.Error("a third run must be refused by a queue of two")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	first, err := q.Next(ctx)
	if err != nil || first.Name != "a" {
		t.Fatalf("first = %v, %v; want the oldest, a", first, err)
	}
	if !q.Add(key("c"), t0.Add(2*time.Second)) {
		t.Error("a freed slot takes the next run")
	}
	q.Remove(key("b"))
	if second, _ := q.Next(ctx); second.Name != "c" {
		t.Errorf("second = %v, want c", second)
	}
	if _, err := q.Next(ctx); err == nil {
		t.Error("an empty queue blocks until the context ends")
	}
}

func TestNextExceptSkipsHeldRuns(t *testing.T) {
	q := NewQueue(0)
	t0 := time.Now()
	q.Add(key("a-1"), t0)
	q.Add(key("b-1"), t0.Add(time.Second))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	heldA := func(k types.NamespacedName) bool { return k.Name[0] == 'a' }
	if k, ok, err := q.NextExcept(ctx, heldA); err != nil || !ok || k.Name != "b-1" {
		t.Fatalf("NextExcept = %v, %v, %v; want b-1, the oldest not held", k, ok, err)
	}
	if _, ok, err := q.NextExcept(ctx, heldA); err != nil || ok {
		t.Fatalf("every pending run held: want false at once, got %v, %v", ok, err)
	}
	if !q.Contains(key("a-1")) {
		t.Error("a held run stays queued")
	}
	select {
	case <-q.Added():
	default:
		t.Error("Added was not signaled")
	}
}

func TestNextWakesOnAdd(t *testing.T) {
	q := NewQueue(0)
	got := make(chan string, 1)
	go func() {
		k, err := q.Next(context.Background())
		if err == nil {
			got <- k.Name
		}
	}()
	time.Sleep(50 * time.Millisecond)
	q.Add(key("late"), time.Now())
	select {
	case n := <-got:
		if n != "late" {
			t.Errorf("got %s", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Next did not wake on Add")
	}
}
