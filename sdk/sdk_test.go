package sdk_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/base698/ecg-demo/evidence"
	"github.com/base698/ecg-demo/internal/testfixture"
	"github.com/base698/ecg-demo/sdk"
)

func TestSDKVersionedChecksAndSingleFinish(t *testing.T) {
	o := testfixture.Observation("one")
	var recorded []evidence.Observation
	c, err := sdk.New(sdk.Options{Release: o.Series.Path[0], Recorder: sdk.RecorderFunc(func(_ context.Context, o evidence.Observation) error { recorded = append(recorded, o); return nil })})
	if err != nil {
		t.Fatal(err)
	}
	ctx, op, err := c.Start(context.Background(), o.Series)
	if err != nil {
		t.Fatal(err)
	}
	check := o.Checks[0]
	check.Status = "failed"
	if err = op.Check(check); err != nil {
		t.Fatal(err)
	}
	check.Status = "passed"
	op.Check(check)
	wrong := check
	wrong.Version = "2"
	if err = op.Check(wrong); err == nil {
		t.Fatal("undeclared check accepted")
	}
	if err = op.End(ctx, sdk.Outcome{Complete: true}); err != nil {
		t.Fatal(err)
	}
	if err = op.End(ctx, sdk.Outcome{Complete: true}); err == nil {
		t.Fatal("duplicate end accepted")
	}
	if len(recorded) != 1 || !recorded[0].Sample().AssertionFailed || recorded[0].ID == "" {
		t.Fatal(recorded)
	}
}

func TestAsyncIsBoundedAndReportsLostTelemetry(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	sink := sdk.RecorderFunc(func(ctx context.Context, o evidence.Observation) error {
		once.Do(func() { close(started) })
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	a, err := sdk.NewAsync(context.Background(), sink, sdk.AsyncOptions{Capacity: 1})
	if err != nil {
		t.Fatal(err)
	}
	o := testfixture.Observation("one")
	if err = a.Record(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	<-started
	o.ID = "two"
	if err = a.Record(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	o.ID = "three"
	if err = a.Record(context.Background(), o); !errors.Is(err, sdk.ErrBufferFull) {
		t.Fatal("buffer not bounded", err)
	}
	close(release)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = a.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if stats := a.Stats(); stats.Accepted != 2 || stats.Delivered != 2 || stats.Dropped != 1 {
		t.Fatal(stats)
	}
	if err = a.Record(context.Background(), o); !errors.Is(err, sdk.ErrClosed) {
		t.Fatal(err)
	}
}

func TestConcurrentRecordAndClose(t *testing.T) {
	a, _ := sdk.NewAsync(context.Background(), sdk.RecorderFunc(func(context.Context, evidence.Observation) error { return nil }), sdk.AsyncOptions{})
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() { defer wg.Done(); _ = a.Record(context.Background(), testfixture.Observation("same")) }()
	}
	_ = a.Close(context.Background())
	wg.Wait()
}

func TestAsyncExportFailureDoesNotBecomeSuccess(t *testing.T) {
	a, _ := sdk.NewAsync(context.Background(), sdk.RecorderFunc(func(context.Context, evidence.Observation) error { return errors.New("offline") }), sdk.AsyncOptions{})
	if err := a.Record(context.Background(), testfixture.Observation("one")); err != nil {
		t.Fatal(err)
	}
	_ = a.Close(context.Background())
	if s := a.Stats(); s.Failed != 1 || s.Delivered != 0 {
		t.Fatal(s)
	}
}

func TestParentCancellationClosesRecorder(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	a, _ := sdk.NewAsync(parent, sdk.RecorderFunc(func(context.Context, evidence.Observation) error { return nil }), sdk.AsyncOptions{})
	cancel()
	deadline := time.Now().Add(time.Second)
	for {
		err := a.Record(context.Background(), testfixture.Observation("cancelled"))
		if errors.Is(err, sdk.ErrClosed) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cancellation did not close recorder")
		}
		time.Sleep(time.Millisecond)
	}
	if err := a.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}
