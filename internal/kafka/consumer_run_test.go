package kafka

import (
	"context"
	"errors"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"
)

type fakeReaderResult struct {
	msg kafkago.Message
	err error
}

type fakeReader struct {
	topic   string
	fetches []fakeReaderResult

	fetchIdx int
	commits  [][]kafkago.Message
	closed   bool

	commitErr error
	closeErr  error
}

func (f *fakeReader) FetchMessage(ctx context.Context) (kafkago.Message, error) {
	if f.fetchIdx >= len(f.fetches) {
		<-ctx.Done()
		return kafkago.Message{}, ctx.Err()
	}
	out := f.fetches[f.fetchIdx]
	f.fetchIdx++
	return out.msg, out.err
}

func (f *fakeReader) CommitMessages(_ context.Context, msgs ...kafkago.Message) error {
	cp := append([]kafkago.Message(nil), msgs...)
	f.commits = append(f.commits, cp)
	return f.commitErr
}

func (f *fakeReader) Close() error {
	f.closed = true
	return f.closeErr
}

func (f *fakeReader) Config() kafkago.ReaderConfig {
	return kafkago.ReaderConfig{Topic: f.topic}
}

type fakeWriter struct {
	writes []kafkago.Message
	closed bool
	err    error
}

func (f *fakeWriter) WriteMessages(_ context.Context, msgs ...kafkago.Message) error {
	f.writes = append(f.writes, msgs...)
	return f.err
}

func (f *fakeWriter) Close() error {
	f.closed = true
	return nil
}

type fakeBatchHandler struct {
	handleFn    func(context.Context, []kafkago.Message) error
	handleOneFn func(context.Context, kafkago.Message) error

	handleCalls    int
	handleOneCalls int
}

func (h *fakeBatchHandler) Handle(ctx context.Context, msgs []kafkago.Message) error {
	h.handleCalls++
	if h.handleFn != nil {
		return h.handleFn(ctx, msgs)
	}
	return nil
}

func (h *fakeBatchHandler) HandleOne(ctx context.Context, msg kafkago.Message) error {
	h.handleOneCalls++
	if h.handleOneFn != nil {
		return h.handleOneFn(ctx, msg)
	}
	return nil
}

func TestFetchBatch_FirstFetchError(t *testing.T) {
	r := &fakeReader{
		fetches: []fakeReaderResult{{err: errors.New("boom")}},
	}
	c := &BatchConsumer{
		Reader:    r,
		BatchSize: 4,
		BatchWait: 50 * time.Millisecond,
	}

	got, err := c.fetchBatch(context.Background())
	if err == nil || err.Error() != "boom" {
		t.Fatalf("expected boom error, got %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no messages, got %d", len(got))
	}
}

func TestFetchBatch_DeadlineAfterFirstMessage(t *testing.T) {
	msg := kafkago.Message{Topic: "t", Offset: 1}
	r := &fakeReader{
		fetches: []fakeReaderResult{
			{msg: msg},
			{err: context.DeadlineExceeded},
		},
	}
	c := &BatchConsumer{
		Reader:    r,
		BatchSize: 4,
		BatchWait: 50 * time.Millisecond,
	}

	got, err := c.fetchBatch(context.Background())
	if err != nil {
		t.Fatalf("fetchBatch: %v", err)
	}
	if len(got) != 1 || got[0].Offset != 1 {
		t.Fatalf("unexpected batch: %+v", got)
	}
}

func TestRun_BatchSuccessAndCommit(t *testing.T) {
	msg := kafkago.Message{Topic: "payments", Offset: 10, Key: []byte("k"), Value: []byte("v")}
	r := &fakeReader{
		topic: "payments",
		fetches: []fakeReaderResult{
			{msg: msg},
			{err: context.DeadlineExceeded},
			{err: context.Canceled},
		},
	}
	w := &fakeWriter{}
	h := &fakeBatchHandler{}

	c := &BatchConsumer{
		Reader:    r,
		BatchSize: 8,
		BatchWait: 10 * time.Millisecond,
		Handler:   h,
		dlq:       w,
	}
	if err := c.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}

	if h.handleCalls != 1 {
		t.Fatalf("Handle calls = %d, want 1", h.handleCalls)
	}
	if h.handleOneCalls != 0 {
		t.Fatalf("HandleOne calls = %d, want 0", h.handleOneCalls)
	}
	if len(r.commits) != 1 || len(r.commits[0]) != 1 {
		t.Fatalf("commits=%d size=%d, want 1/1", len(r.commits), len(r.commits[0]))
	}
	if !r.closed || !w.closed {
		t.Fatal("Run must close reader and dlq writer")
	}
}

func TestRun_FallbackToPerMessageAndDLQ(t *testing.T) {
	m1 := kafkago.Message{Topic: "payments", Offset: 21, Key: []byte("bad"), Value: []byte("v1")}
	m2 := kafkago.Message{Topic: "payments", Offset: 22, Key: []byte("ok"), Value: []byte("v2")}
	r := &fakeReader{
		topic: "payments",
		fetches: []fakeReaderResult{
			{msg: m1},
			{msg: m2},
			{err: context.DeadlineExceeded},
			{err: context.Canceled},
		},
	}
	w := &fakeWriter{}
	h := &fakeBatchHandler{
		handleFn: func(context.Context, []kafkago.Message) error {
			return errors.New("batch failed")
		},
		handleOneFn: func(_ context.Context, msg kafkago.Message) error {
			if string(msg.Key) == "bad" {
				return errors.New("poison message")
			}
			return nil
		},
	}

	c := &BatchConsumer{
		Reader:    r,
		BatchSize: 8,
		BatchWait: 10 * time.Millisecond,
		Handler:   h,
		dlq:       w,
	}
	if err := c.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}

	if h.handleCalls != 1 {
		t.Fatalf("Handle calls = %d, want 1", h.handleCalls)
	}
	if h.handleOneCalls != 2 {
		t.Fatalf("HandleOne calls = %d, want 2", h.handleOneCalls)
	}
	if len(r.commits) != 1 || len(r.commits[0]) != 2 {
		t.Fatalf("expected one commit for two messages, got commits=%d size=%d", len(r.commits), len(r.commits[0]))
	}
	if len(w.writes) != 1 {
		t.Fatalf("DLQ writes = %d, want 1", len(w.writes))
	}
	if string(w.writes[0].Key) != "bad" {
		t.Fatalf("DLQ key = %q, want bad", string(w.writes[0].Key))
	}
}
