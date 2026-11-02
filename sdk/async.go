package sdk

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/base698/ecg-demo/evidence"
)

var ErrBufferFull = errors.New("ECG observation buffer is full")
var ErrClosed = errors.New("ECG recorder is closed")

type AsyncOptions struct {
	Capacity int
	Timeout  time.Duration
}
type ExportStats struct{ Accepted, Delivered, Failed, Dropped uint64 }
type Async struct {
	mu                                   sync.Mutex
	closed                               bool
	queue                                chan evidence.Observation
	done                                 chan struct{}
	cancel                               context.CancelFunc
	accepted, delivered, failed, dropped atomic.Uint64
}

func NewAsync(parent context.Context, downstream Recorder, o AsyncOptions) (*Async, error) {
	if downstream == nil {
		return nil, fmt.Errorf("recorder is required")
	}
	if o.Capacity <= 0 {
		o.Capacity = 256
	}
	if o.Timeout <= 0 {
		o.Timeout = 3 * time.Second
	}
	ctx, cancel := context.WithCancel(parent)
	a := &Async{queue: make(chan evidence.Observation, o.Capacity), done: make(chan struct{}), cancel: cancel}
	go func() {
		defer close(a.done)
		defer cancel()
		for {
			var observation evidence.Observation
			select {
			case <-ctx.Done():
				a.stop()
				for range a.queue {
					a.dropped.Add(1)
				}
				return
			case item, ok := <-a.queue:
				if !ok {
					return
				}
				observation = item
			}
			if ctx.Err() != nil {
				a.dropped.Add(1)
				continue
			}
			callCtx, end := context.WithTimeout(ctx, o.Timeout)
			err := downstream.Record(callCtx, observation)
			end()
			if err != nil {
				a.failed.Add(1)
			} else {
				a.delivered.Add(1)
			}
		}
	}()
	return a, nil
}

func (a *Async) Record(ctx context.Context, o evidence.Observation) error {
	if err := ctx.Err(); err != nil {
		a.dropped.Add(1)
		return err
	}
	if err := o.Validate(); err != nil {
		return err
	}
	o = o.Canonical()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		a.dropped.Add(1)
		return ErrClosed
	}
	select {
	case a.queue <- o:
		a.accepted.Add(1)
		return nil
	default:
		a.dropped.Add(1)
		return ErrBufferFull
	}
}
func (a *Async) Stats() ExportStats {
	return ExportStats{a.accepted.Load(), a.delivered.Load(), a.failed.Load(), a.dropped.Load()}
}

// Close drains the bounded queue. A deadline cancels in-flight exports. The
// supplied recorder must honor context cancellation, as the HTTP client does.
func (a *Async) stop() {
	a.mu.Lock()
	if !a.closed {
		a.closed = true
		close(a.queue)
	}
	a.mu.Unlock()
}

func (a *Async) Close(ctx context.Context) error {
	a.stop()
	select {
	case <-a.done:
		a.cancel()
		return nil
	case <-ctx.Done():
		a.cancel()
		return ctx.Err()
	}
}
