package engine

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

type batchTestItem struct {
	id int
}

func TestRunBatchedDBWriter(t *testing.T) {
	t.Run("flushes on batch size", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		inCh := make(chan batchTestItem, 2)
		batches := make(chan []int, 1)
		done := make(chan struct{})

		go func() {
			runBatchedDBWriter(ctx, inCh, 2, time.Hour, testLogger(), batchWriteOps[batchTestItem]{
				batchInsert: func(_ context.Context, items []batchTestItem) error {
					batches <- collectIDs(items)
					return nil
				},
				singleInsert: func(context.Context, batchTestItem) error { return nil },
				itemAttrs:    func(batchTestItem) []any { return nil },
			})
			close(done)
		}()

		inCh <- batchTestItem{id: 1}
		inCh <- batchTestItem{id: 2}

		select {
		case got := <-batches:
			want := []int{1, 2}
			if !slices.Equal(got, want) {
				t.Fatalf("unexpected batch: got %v want %v", got, want)
			}
		case <-time.After(time.Second):
			t.Fatal("timeout waiting for batch flush")
		}

		cancel()

		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("timeout waiting for writer shutdown")
		}
	})

	t.Run("flushes on timer", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		inCh := make(chan batchTestItem, 1)
		batches := make(chan []int, 1)
		done := make(chan struct{})

		go func() {
			runBatchedDBWriter(ctx, inCh, 10, 20*time.Millisecond, testLogger(), batchWriteOps[batchTestItem]{
				batchInsert: func(_ context.Context, items []batchTestItem) error {
					batches <- collectIDs(items)
					return nil
				},
				singleInsert: func(context.Context, batchTestItem) error { return nil },
				itemAttrs:    func(batchTestItem) []any { return nil },
			})
			close(done)
		}()

		inCh <- batchTestItem{id: 7}

		select {
		case got := <-batches:
			want := []int{7}
			if !slices.Equal(got, want) {
				t.Fatalf("unexpected batch: got %v want %v", got, want)
			}
		case <-time.After(time.Second):
			t.Fatal("timeout waiting for timer flush")
		}

		cancel()

		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("timeout waiting for writer shutdown")
		}
	})

	t.Run("falls back to individual inserts", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		inCh := make(chan batchTestItem, 2)
		batchCalls := make(chan struct{}, 1)
		singleItems := make(chan int, 2)
		done := make(chan struct{})

		go func() {
			runBatchedDBWriter(ctx, inCh, 2, time.Hour, testLogger(), batchWriteOps[batchTestItem]{
				batchInsert: func(context.Context, []batchTestItem) error {
					batchCalls <- struct{}{}
					return errors.New("boom")
				},
				singleInsert: func(_ context.Context, item batchTestItem) error {
					singleItems <- item.id
					return nil
				},
				itemAttrs: func(item batchTestItem) []any {
					return []any{"id", item.id}
				},
			})
			close(done)
		}()

		inCh <- batchTestItem{id: 3}
		inCh <- batchTestItem{id: 4}

		select {
		case <-batchCalls:
		case <-time.After(time.Second):
			t.Fatal("timeout waiting for batch insert")
		}

		got := []int{<-singleItems, <-singleItems}
		slices.Sort(got)
		want := []int{3, 4}
		if !slices.Equal(got, want) {
			t.Fatalf("unexpected single insert order: got %v want %v", got, want)
		}

		cancel()

		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("timeout waiting for writer shutdown")
		}
	})

	t.Run("flushes remaining items on shutdown", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		inCh := make(chan batchTestItem, 1)
		batches := make(chan []int, 1)
		done := make(chan struct{})

		go func() {
			runBatchedDBWriter(ctx, inCh, 10, time.Hour, testLogger(), batchWriteOps[batchTestItem]{
				batchInsert: func(_ context.Context, items []batchTestItem) error {
					batches <- collectIDs(items)
					return nil
				},
				singleInsert: func(context.Context, batchTestItem) error { return nil },
				itemAttrs:    func(batchTestItem) []any { return nil },
			})
			close(done)
		}()

		inCh <- batchTestItem{id: 9}
		cancel()

		select {
		case got := <-batches:
			want := []int{9}
			if !slices.Equal(got, want) {
				t.Fatalf("unexpected batch: got %v want %v", got, want)
			}
		case <-time.After(time.Second):
			t.Fatal("timeout waiting for shutdown flush")
		}

		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("timeout waiting for writer shutdown")
		}
	})
}

func collectIDs(items []batchTestItem) []int {
	ids := make([]int, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.id)
	}
	return ids
}
