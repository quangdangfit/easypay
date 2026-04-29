package domain

import "testing"

func TestOrderStatus_Valid(t *testing.T) {
	valid := []OrderStatus{
		OrderStatusCreated, OrderStatusPending, OrderStatusPaid,
		OrderStatusFailed, OrderStatusExpired, OrderStatusRefunded,
	}
	for _, s := range valid {
		if !s.Valid() {
			t.Errorf("expected %q valid", s)
		}
	}
	for _, s := range []OrderStatus{"", "unknown", "PAID"} {
		if s.Valid() {
			t.Errorf("expected %q invalid", s)
		}
	}
}
