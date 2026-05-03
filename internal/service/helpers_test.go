package service

import "testing"

func TestWithOrderQuery(t *testing.T) {
	cases := []struct {
		name, base, order, want string
	}{
		{"empty base", "", "ord-1", ""},
		{"no existing query", "https://x.test/ok", "ord-1", "https://x.test/ok?order_id=ord-1"},
		{"existing query keeps &", "https://x.test/ok?a=b", "ord-1", "https://x.test/ok?a=b&order_id=ord-1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := withOrderQuery(c.base, c.order)
			if got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestDefaultIfEmpty(t *testing.T) {
	def := []string{"card"}
	if got := defaultIfEmpty(nil, def); got[0] != "card" {
		t.Fatal("nil should fall back to default")
	}
	if got := defaultIfEmpty([]string{}, def); got[0] != "card" {
		t.Fatal("empty should fall back to default")
	}
	if got := defaultIfEmpty([]string{"klarna"}, def); got[0] != "klarna" {
		t.Fatal("non-empty should be passed through")
	}
}

func TestOrFallback(t *testing.T) {
	if got := orFallback("", "def"); got != "def" {
		t.Fatalf("got %q, want def", got)
	}
	if got := orFallback("v", "def"); got != "v" {
		t.Fatalf("got %q, want v", got)
	}
}

func TestPrimaryMethod(t *testing.T) {
	in := CreatePaymentInput{Method: "crypto"}
	if got := primaryMethod(in); got != "crypto_eth" {
		t.Fatalf("got %q, want crypto_eth", got)
	}
	in = CreatePaymentInput{PaymentMethodTypes: []string{"klarna", "card"}}
	if got := primaryMethod(in); got != "klarna" {
		t.Fatalf("got %q, want klarna", got)
	}
	if got := primaryMethod(CreatePaymentInput{}); got != "card" {
		t.Fatalf("default got %q, want card", got)
	}
}
