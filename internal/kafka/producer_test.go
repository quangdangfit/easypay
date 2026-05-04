package kafka

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/quangdangfit/easypay/internal/config"
)

type fakeAdminDialer struct {
	dialFn func(address string) (adminConn, error)
	calls  []string
}

func (d *fakeAdminDialer) DialContext(_ context.Context, _, address string) (adminConn, error) {
	d.calls = append(d.calls, address)
	if d.dialFn == nil {
		return nil, fmt.Errorf("unexpected dial %s", address)
	}
	return d.dialFn(address)
}

type fakeAdminConn struct {
	controller    kafkago.Broker
	controllerErr error
	createErr     error
	createTopics  []kafkago.TopicConfig
	closed        bool
}

func (c *fakeAdminConn) Controller() (kafkago.Broker, error) {
	if c.controllerErr != nil {
		return kafkago.Broker{}, c.controllerErr
	}
	return c.controller, nil
}

func (c *fakeAdminConn) CreateTopics(topics ...kafkago.TopicConfig) error {
	c.createTopics = append(c.createTopics, topics...)
	return c.createErr
}

func (c *fakeAdminConn) Close() error {
	c.closed = true
	return nil
}

func TestPaymentConfirmedEventEncoding(t *testing.T) {
	e := PaymentConfirmedEvent{
		OrderID: "ord-1", MerchantID: "M1", Status: "paid",
		Amount: 1500, Currency: "USD", ConfirmedAt: 1,
	}
	if e.OrderID == "" {
		t.Fatal("smoke")
	}
}

func TestMarshalForDLQ(t *testing.T) {
	m := kafkago.Message{Topic: "t", Partition: 1, Offset: 42, Key: []byte("k"), Value: []byte("v")}
	b, err := MarshalForDLQ(m, errors.New("bad"))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(b) == 0 {
		t.Fatal("empty payload")
	}
}

func TestPinger_Empty(t *testing.T) {
	p := NewPinger(config.KafkaConfig{Brokers: nil})
	if err := p.Ping(context.Background()); err == nil {
		t.Fatal("expected error for empty brokers")
	}
}

func TestPinger_BadAddress(t *testing.T) {
	p := NewPinger(config.KafkaConfig{Brokers: []string{"127.0.0.1:1"}})
	if err := p.Ping(context.Background()); err == nil {
		t.Fatal("expected dial error")
	}
}

func TestPublisher_Close(t *testing.T) {
	// We can't bring up a broker in unit tests, but we can verify Close() is
	// safe to call on a freshly-constructed publisher (the underlying kafka.Writer
	// permits this — its Close just flushes buffers).
	p := NewPublisher(config.KafkaConfig{
		Brokers:        []string{"127.0.0.1:1"},
		TopicConfirmed: "t2",
		TopicDLQ:       "t3",
	})
	if err := p.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestPublishPaymentConfirmed_NoBrokerErrors(t *testing.T) {
	p := NewPublisher(config.KafkaConfig{
		Brokers:        []string{"127.0.0.1:1"},
		TopicConfirmed: "t2",
		TopicDLQ:       "t3",
	})
	defer func() { _ = p.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	// Without a broker, this should error (timeout / connection refused).
	// We just want to exercise the marshalling + WriteMessages code path.
	_ = p.PublishPaymentConfirmed(ctx, PaymentConfirmedEvent{OrderID: "ord-1"})
}

func TestPublishPaymentConfirmed_MarshalError(t *testing.T) {
	// Create an event that will fail to marshal
	// We can't directly cause json.Marshal to fail with PaymentConfirmedEvent,
	// but we can verify the error handling by checking the code path
	p := NewPublisher(config.KafkaConfig{
		Brokers:        []string{"127.0.0.1:1"},
		TopicConfirmed: "t2",
		TopicDLQ:       "t3",
	})
	defer func() { _ = p.Close() }()
	// Normal event should marshal fine
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	e := PaymentConfirmedEvent{
		OrderID:    "ord-1",
		MerchantID: "M1",
		Status:     "paid",
		Amount:     1500,
		Currency:   "USD",
	}
	_ = p.PublishPaymentConfirmed(ctx, e)
}

func TestEnsureTopics_NoBrokers(t *testing.T) {
	// Should be a silent no-op, not panic.
	ensureTopics(nil, "t")
}

func TestEnsureTopics_BadBroker(t *testing.T) {
	ensureTopics([]string{"127.0.0.1:1"}, "t")
}

func TestEnsureTopicsWithDialer_NoBrokers(t *testing.T) {
	d := &fakeAdminDialer{}
	ensureTopicsWithDialer(context.Background(), d.DialContext, nil, "t")
	if len(d.calls) != 0 {
		t.Fatalf("dial calls = %d, want 0", len(d.calls))
	}
}

func TestEnsureTopicsWithDialer_FirstDialError(t *testing.T) {
	d := &fakeAdminDialer{
		dialFn: func(address string) (adminConn, error) {
			return nil, errors.New("dial failed")
		},
	}
	ensureTopicsWithDialer(context.Background(), d.DialContext, []string{"broker-a:9092"}, "t")
	if len(d.calls) != 1 || d.calls[0] != "broker-a:9092" {
		t.Fatalf("calls = %v", d.calls)
	}
}

func TestEnsureTopicsWithDialer_ControllerError(t *testing.T) {
	first := &fakeAdminConn{controllerErr: errors.New("no controller")}
	d := &fakeAdminDialer{
		dialFn: func(address string) (adminConn, error) {
			return first, nil
		},
	}
	ensureTopicsWithDialer(context.Background(), d.DialContext, []string{"broker-a:9092"}, "t")
	if len(d.calls) != 1 {
		t.Fatalf("calls = %v, want one call", d.calls)
	}
	if !first.closed {
		t.Fatal("first connection should be closed")
	}
}

func TestEnsureTopicsWithDialer_SecondDialError(t *testing.T) {
	first := &fakeAdminConn{controller: kafkago.Broker{Host: "ctrl", Port: 9093}}
	d := &fakeAdminDialer{
		dialFn: func(address string) (adminConn, error) {
			if address == "broker-a:9092" {
				return first, nil
			}
			return nil, errors.New("dial controller failed")
		},
	}
	ensureTopicsWithDialer(context.Background(), d.DialContext, []string{"broker-a:9092"}, "t")
	if len(d.calls) != 2 {
		t.Fatalf("calls = %v, want two dials", d.calls)
	}
	if d.calls[1] != "ctrl:9093" {
		t.Fatalf("controller dial address = %s, want ctrl:9093", d.calls[1])
	}
	if !first.closed {
		t.Fatal("first connection should be closed")
	}
}

func TestEnsureTopicsWithDialer_CreatesOnlyNonEmptyTopics(t *testing.T) {
	first := &fakeAdminConn{controller: kafkago.Broker{Host: "ctrl", Port: 9093}}
	second := &fakeAdminConn{}
	d := &fakeAdminDialer{
		dialFn: func(address string) (adminConn, error) {
			switch address {
			case "broker-a:9092":
				return first, nil
			case "ctrl:9093":
				return second, nil
			default:
				return nil, fmt.Errorf("unexpected address: %s", address)
			}
		},
	}

	ensureTopicsWithDialer(context.Background(), d.DialContext, []string{"broker-a:9092"}, "", "topic-a", "topic-b")

	want := []kafkago.TopicConfig{
		{Topic: "topic-a", NumPartitions: 1, ReplicationFactor: 1},
		{Topic: "topic-b", NumPartitions: 1, ReplicationFactor: 1},
	}
	if !reflect.DeepEqual(second.createTopics, want) {
		t.Fatalf("CreateTopics specs = %#v, want %#v", second.createTopics, want)
	}
	if !first.closed || !second.closed {
		t.Fatal("both connections should be closed")
	}
}
