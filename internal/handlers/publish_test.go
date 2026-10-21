package handlers

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"real-time-ui-update-microservice/internal/hub"
)

type broadcastCall struct {
	scope   hub.Scope
	channel string
	payload string
}

type fakeBroadcaster struct {
	calls []broadcastCall
}

func (f *fakeBroadcaster) Broadcast(scope hub.Scope, channel string, payload []byte) int {
	f.calls = append(f.calls, broadcastCall{scope, channel, string(payload)})
	return 1
}

func publish(b Broadcaster, scope hub.Scope, limit int64, target string, body io.Reader) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, target, body)
	rec := httptest.NewRecorder()
	Publish(b, scope, limit).ServeHTTP(rec, req)
	return rec
}

func TestPublishBroadcastsValidJSON(t *testing.T) {
	bodies := []string{
		`{"id":"order-1"}`,
		`[]`,
		`[1,"two",{"three":3}]`,
		`"string"`,
		`42`,
		`-1.5e10`,
		`true`,
		`null`,
		` {"padded": true} `,
		"{\n\t\"multiline\": 1\n}",
		`{"unicode":"日本語","emoji":"🚀","escape":"é"}`,
	}
	for _, scope := range []hub.Scope{hub.Public, hub.Private} {
		for _, body := range bodies {
			b := &fakeBroadcaster{}
			rec := publish(b, scope, 1<<20, "/update?channel=orders", strings.NewReader(body))
			if rec.Code != http.StatusAccepted {
				t.Errorf("scope %d body %q: status = %d, want 202", scope, body, rec.Code)
				continue
			}
			want := broadcastCall{scope, "orders", body}
			if len(b.calls) != 1 || b.calls[0] != want {
				t.Errorf("calls = %+v, want [%+v]", b.calls, want)
			}
			if rec.Body.Len() != 0 {
				t.Errorf("202 response has body %q", rec.Body.String())
			}
		}
	}
}

func TestPublishChannelSelection(t *testing.T) {
	tests := map[string]string{
		"/update":                            "default",
		"/update?channel=":                   "default",
		"/update?channel=orders":             "orders",
		"/update?channel=Orders":             "Orders",
		"/update?channel=a%20b":              "a b",
		"/update?channel=%E6%97%A5%E6%9C%AC": "日本",
		"/update?channel=a/b/c":              "a/b/c",
		"/update?channel=x&channel=y":        "x",
		"/update?other=1&channel=z":          "z",
		"/update?channel=..%2F..%2Fetc":      "../../etc",
	}
	for target, want := range tests {
		b := &fakeBroadcaster{}
		rec := publish(b, hub.Private, 1<<20, target, strings.NewReader(`{}`))
		if rec.Code != http.StatusAccepted || len(b.calls) != 1 || b.calls[0].channel != want {
			t.Errorf("%s: status %d calls %+v, want channel %q", target, rec.Code, b.calls, want)
		}
	}
}

func TestPublishRejectsInvalidJSON(t *testing.T) {
	bodies := []string{
		"",
		" ",
		"{",
		"}",
		`{"a":}`,
		`{"a":1,}`,
		`{'a':1}`,
		`{"a":1}{"b":2}`,
		`{"a":1} trailing`,
		`undefined`,
		`NaN`,
		"\xff\xfe",
		`{"a":"unterminated}`,
		"<xml/>",
	}
	for _, body := range bodies {
		b := &fakeBroadcaster{}
		rec := publish(b, hub.Public, 1<<20, "/publish", strings.NewReader(body))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %q: status = %d, want 400", body, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "Invalid JSON") {
			t.Errorf("body %q: response %q", body, rec.Body.String())
		}
		if len(b.calls) != 0 {
			t.Errorf("body %q was broadcast", body)
		}
	}
}

func TestPublishBodyLimit(t *testing.T) {
	const limit = 64
	exact := `{"k":"` + strings.Repeat("x", limit-8) + `"}`
	if len(exact) != limit {
		t.Fatalf("fixture length %d", len(exact))
	}

	b := &fakeBroadcaster{}
	if rec := publish(b, hub.Public, limit, "/publish", strings.NewReader(exact)); rec.Code != http.StatusAccepted {
		t.Errorf("body at limit: status = %d, want 202", rec.Code)
	}

	over := `{"k":"` + strings.Repeat("x", limit-7) + `"}`
	b = &fakeBroadcaster{}
	rec := publish(b, hub.Public, limit, "/publish", strings.NewReader(over))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("body over limit: status = %d, want 413", rec.Code)
	}
	if len(b.calls) != 0 {
		t.Error("oversized body was broadcast")
	}

	b = &fakeBroadcaster{}
	rec = publish(b, hub.Public, limit, "/publish", io.LimitReader(infiniteReader{}, 10<<20))
	if rec.Code != http.StatusRequestEntityTooLarge || len(b.calls) != 0 {
		t.Errorf("huge body: status = %d, calls = %d", rec.Code, len(b.calls))
	}
}

func TestPublishBodyReadError(t *testing.T) {
	b := &fakeBroadcaster{}
	rec := publish(b, hub.Public, 1<<20, "/publish", errReader{})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Error reading body") {
		t.Errorf("status = %d body = %q", rec.Code, rec.Body.String())
	}
	if len(b.calls) != 0 {
		t.Error("broadcast after read error")
	}
}

type infiniteReader struct{}

func (infiniteReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = '1'
	}
	return len(p), nil
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

func BenchmarkPublish(b *testing.B) {
	handler := Publish(&discardBroadcaster{}, hub.Public, 1<<20)
	body := `{"id":"order-123","item":"Demo","amount":10,"tags":["a","b","c"]}`
	b.ReportAllocs()
	for b.Loop() {
		req := httptest.NewRequest(http.MethodPost, "/publish?channel=news", strings.NewReader(body))
		handler.ServeHTTP(httptest.NewRecorder(), req)
	}
}

type discardBroadcaster struct{}

func (discardBroadcaster) Broadcast(hub.Scope, string, []byte) int { return 0 }
