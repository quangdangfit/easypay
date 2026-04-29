package hmac

import "testing"

func TestSignVerifyRoundtrip(t *testing.T) {
	secret := "topsecret"
	payload := []byte(`{"amount":1500,"currency":"USD"}`)
	sig := Sign(secret, payload)
	if !Verify(secret, payload, sig) {
		t.Fatal("expected signature to verify")
	}
}

func TestVerifyRejectsTamperedPayload(t *testing.T) {
	secret := "topsecret"
	sig := Sign(secret, []byte("hello"))
	if Verify(secret, []byte("hellO"), sig) {
		t.Fatal("expected tampered payload to fail")
	}
}

func TestVerifyRejectsBadSignature(t *testing.T) {
	if Verify("s", []byte("p"), "not-hex") {
		t.Fatal("expected non-hex signature to fail")
	}
}

func TestVerifyRejectsWrongSecret(t *testing.T) {
	sig := Sign("a", []byte("p"))
	if Verify("b", []byte("p"), sig) {
		t.Fatal("expected wrong secret to fail")
	}
}
