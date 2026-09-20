package main

import (
	"context"
	"sync"
)

// A synchronous host callback cannot be interrupted by context cancellation.
// Keep a permanently blocked callback from consuming a new goroutine per timeout.
const maxHostCallbackConcurrency = 8

type hostCallbackBoundary struct {
	slots  chan struct{}
	mu     sync.Mutex
	cond   *sync.Cond
	active int
}

func newHostCallbackBoundary(limit int) *hostCallbackBoundary {
	boundary := &hostCallbackBoundary{slots: make(chan struct{}, limit)}
	boundary.cond = sync.NewCond(&boundary.mu)
	return boundary
}

var commandCodeHostCallbackBoundary = newHostCallbackBoundary(maxHostCallbackConcurrency)

func invokeBoundedHostCallback(ctx context.Context, callback func() ([]byte, error)) ([]byte, error) {
	return commandCodeHostCallbackBoundary.invoke(ctx, callback)
}

func (b *hostCallbackBoundary) invoke(ctx context.Context, callback func() ([]byte, error)) ([]byte, error) {
	ctx = contextOrBackground(ctx)
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}
	select {
	case b.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := ctxErr(ctx); err != nil {
		<-b.slots
		return nil, err
	}

	b.mu.Lock()
	b.active++
	b.mu.Unlock()

	result := make(chan hostCallbackResult, 1)
	go func() {
		defer func() {
			<-b.slots
			b.mu.Lock()
			b.active--
			if b.active == 0 {
				b.cond.Broadcast()
			}
			b.mu.Unlock()
		}()
		raw, err := callback()
		result <- hostCallbackResult{raw: raw, err: err}
	}()

	select {
	case result := <-result:
		return result.raw, result.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (b *hostCallbackBoundary) wait() {
	// Callers must quiesce plugin work before waiting so no new callback can be admitted.
	b.mu.Lock()
	for b.active > 0 {
		b.cond.Wait()
	}
	b.mu.Unlock()
}

type hostCallbackResult struct {
	raw []byte
	err error
}
