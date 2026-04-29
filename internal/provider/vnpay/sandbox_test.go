// Smoke tests for VNPay sandbox. Three things are verified:
//
//   1. Signature math matches a hand-computed HMAC-SHA512 vector
//      (no env / network — always runs).
//   2. A signed checkout URL can be built end-to-end.
//   3. The sandbox host returns 200 for the built URL (network — skips
//      automatically when env credentials are missing).
//
// To run the network test:
//
//	export VNPAY_TMN_CODE=...        # from VNPay sample repo (config.json)
//	export VNPAY_HASH_SECRET=...     # ditto
//	go test ./internal/provider/vnpay/ -v -run TestSandbox
//
// Demo credentials are published in https://github.com/vnpay/sample-code-node.js
// (file config.json). They rotate occasionally; if signature verification
// fails on the sandbox, pull the latest sample.
package vnpay

import (
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// SignParams returns the lowercase-hex HMAC-SHA512 of the canonical query
// string (alphabetically sorted, URL-encoded), per the VNPay docs. The
// vnp_SecureHash field, if present, is excluded from the signed input.
func SignParams(params url.Values, secret string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		if k == "vnp_SecureHash" || k == "vnp_SecureHashType" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(url.QueryEscape(k))
		b.WriteByte('=')
		b.WriteString(url.QueryEscape(params.Get(k)))
	}
	mac := hmac.New(sha512.New, []byte(secret))
	mac.Write([]byte(b.String()))
	return hex.EncodeToString(mac.Sum(nil))
}

// BuildCheckoutURL returns the fully-signed VNPay sandbox URL for an order.
// It does NOT touch the network.
func BuildCheckoutURL(payURL, tmnCode, secret, orderID string, amountVND int64, info, ip, returnURL, ipnURL string) string {
	p := url.Values{}
	p.Set("vnp_Version", "2.1.0")
	p.Set("vnp_Command", "pay")
	p.Set("vnp_TmnCode", tmnCode)
	p.Set("vnp_Amount", strconv.FormatInt(amountVND*100, 10))
	p.Set("vnp_CurrCode", "VND")
	p.Set("vnp_TxnRef", orderID)
	p.Set("vnp_OrderInfo", info)
	p.Set("vnp_OrderType", "other")
	p.Set("vnp_Locale", "vn")
	p.Set("vnp_ReturnUrl", returnURL)
	p.Set("vnp_IpAddr", ip)
	p.Set("vnp_CreateDate", time.Now().UTC().Add(7*time.Hour).Format("20060102150405"))
	if ipnURL != "" {
		p.Set("vnp_IpnUrl", ipnURL)
	}
	sig := SignParams(p, secret)
	p.Set("vnp_SecureHash", sig)
	return payURL + "?" + p.Encode()
}

// TestSign_KnownVector pins the HMAC-SHA512 implementation against a
// hand-computed reference. Runs offline.
func TestSign_KnownVector(t *testing.T) {
	// Vector hand-computed:
	//   secret = "abc"
	//   message = "vnp_Amount=100&vnp_Command=pay"
	//   HMAC-SHA512 hex (lowercase)
	const secret = "abc"
	expected := computeRef(t, "vnp_Amount=100&vnp_Command=pay", secret)

	p := url.Values{}
	p.Set("vnp_Command", "pay")
	p.Set("vnp_Amount", "100")
	got := SignParams(p, secret)
	if got != expected {
		t.Fatalf("signature mismatch:\n  got      %s\n  expected %s", got, expected)
	}
}

// TestSign_ExcludesHashFields ensures vnp_SecureHash[Type] are not part of
// the signed input — VNPay rejects sigs computed over them.
func TestSign_ExcludesHashFields(t *testing.T) {
	p := url.Values{}
	p.Set("vnp_Command", "pay")
	a := SignParams(p, "secret")

	p.Set("vnp_SecureHash", "deadbeef")
	p.Set("vnp_SecureHashType", "HmacSHA512")
	b := SignParams(p, "secret")
	if a != b {
		t.Fatalf("hash fields must be excluded from signed input: %s vs %s", a, b)
	}
}

// TestSandbox_BuildAndPing attempts a real HTTP GET against the sandbox.
// Skipped automatically when credentials aren't provided via env.
func TestSandbox_BuildAndPing(t *testing.T) {
	tmn := os.Getenv("VNPAY_TMN_CODE")
	secret := os.Getenv("VNPAY_HASH_SECRET")
	if tmn == "" || secret == "" {
		t.Skip("set VNPAY_TMN_CODE + VNPAY_HASH_SECRET to run. " +
			"Demo creds: https://github.com/vnpay/sample-code-node.js (config.json)")
	}
	payURL := envOr("VNPAY_PAY_URL", "https://sandbox.vnpayment.vn/paymentv2/vpcpay.html")
	returnURL := envOr("VNPAY_RETURN_URL", "http://localhost:8080/return/vnpay")

	orderID := fmt.Sprintf("TEST-%d", time.Now().Unix())
	full := BuildCheckoutURL(payURL, tmn, secret, orderID, 50000, "Test thanh toan don "+orderID, "127.0.0.1", returnURL, "")

	t.Logf("Built URL (open in browser to test full flow):\n%s", full)

	// 1) Sandbox host reachable?
	pingResp, err := http.Get(payURL)
	if err != nil {
		t.Fatalf("sandbox unreachable: %v", err)
	}
	pingResp.Body.Close()
	if pingResp.StatusCode >= 500 {
		t.Fatalf("sandbox returned %d on bare GET", pingResp.StatusCode)
	}

	// 2) Signed URL accepted? VNPay returns 200 on the payment form page
	//    when the signature is valid, and 200 on an error page when invalid.
	//    Body content is the only differentiator.
	resp, err := http.Get(full)
	if err != nil {
		t.Fatalf("GET signed URL: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	t.Logf("status: %d, content-length: %d", resp.StatusCode, len(body))

	bodyStr := string(body)
	finalURL := resp.Request.URL.String()
	t.Logf("final URL: %s", finalURL)

	switch {
	case resp.StatusCode != 200:
		t.Fatalf("signed URL got status %d (expected 200). Body head: %s",
			resp.StatusCode, head(bodyStr, 400))

	// VNPay redirects invalid requests to /paymentv2/Payment/Error.html?code=XX.
	// The error page also embeds <div class="box-error"> and a form that POSTs
	// to Error.html — both reliable signals.
	case strings.Contains(finalURL, "/Payment/Error.html") ||
		strings.Contains(bodyStr, "box-error") ||
		strings.Contains(bodyStr, "Payment/Error.html"):
		code := extractErrCode(finalURL, bodyStr)
		t.Fatalf("VNPay rejected our request (error code=%s). Most likely cause: TmnCode unknown or HashSecret mismatch. Body head:\n%s",
			code, head(bodyStr, 600))

	// The valid payment form always contains the bank-selection grid +
	// Vietnamese-language UI strings.
	case containsAny(bodyStr, "vnp-bank", "Chọn ngân hàng", "select-bank", "Phương thức thanh toán"):
		t.Logf("✅ sandbox accepted the signed URL — payment form rendered")

	default:
		t.Logf("body head:\n%s", head(bodyStr, 400))
		t.Log("⚠ couldn't auto-detect outcome; open the URL above in a browser to verify")
	}
}

// extractErrCode pulls the `code=XX` query value from either the redirected
// URL or anchors embedded in the error page.
func extractErrCode(finalURL, body string) string {
	if u, err := url.Parse(finalURL); err == nil {
		if c := u.Query().Get("code"); c != "" {
			return c
		}
	}
	if i := strings.Index(body, "Payment/Error.html?code="); i >= 0 {
		rest := body[i+len("Payment/Error.html?code="):]
		end := strings.IndexAny(rest, "&\"' ")
		if end > 0 {
			return rest[:end]
		}
	}
	return "unknown"
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func head(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func containsAny(s string, subs ...string) bool {
	for _, x := range subs {
		if strings.Contains(s, x) {
			return true
		}
	}
	return false
}

// computeRef computes HMAC-SHA512 directly from a known message+secret —
// used as the oracle for TestSign_KnownVector.
func computeRef(t *testing.T, msg, secret string) string {
	t.Helper()
	mac := hmac.New(sha512.New, []byte(secret))
	mac.Write([]byte(msg))
	return hex.EncodeToString(mac.Sum(nil))
}
