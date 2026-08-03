package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCSRFProtection 非 JSON Content-Type 的 POST 必须被拒绝（防跨站表单伪造）。
func TestCSRFProtection(t *testing.T) {
	s := newTestServer()
	cases := []struct {
		ct   string
		body string
	}{
		{"application/x-www-form-urlencoded", "mode=recv"},
		{"text/plain", "mode=recv"},
		{"", "mode=recv"},
	}
	for _, c := range cases {
		req := httptest.NewRequest("POST", "/api/stop", strings.NewReader(c.body))
		if c.ct != "" {
			req.Header.Set("Content-Type", c.ct)
		}
		rec := httptest.NewRecorder()
		s.handleStop(rec, req)
		if rec.Code != http.StatusUnsupportedMediaType {
			t.Fatalf("Content-Type %q: code = %d, want 415", c.ct, rec.Code)
		}
	}
}

// TestOversizedBodyRejected 超大请求体必须被拒绝。
func TestOversizedBodyRejected(t *testing.T) {
	s := newTestServer()
	big := `{"flows":[` + strings.Repeat("0", 2<<20) + `]}`
	req := httptest.NewRequest("POST", "/api/config?mode=send", strings.NewReader(big))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handleConfig(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("超大 body: code = %d, want 400", rec.Code)
	}
}

// TestValidJSONAccepted 正常 JSON POST 正常处理。
func TestValidJSONAccepted(t *testing.T) {
	s := newTestServer()
	req := httptest.NewRequest("POST", "/api/stop", strings.NewReader(`{"mode":"recv"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handleStop(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("正常 JSON: code = %d, want 200", rec.Code)
	}
}
