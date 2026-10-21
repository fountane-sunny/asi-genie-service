package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLoggingWrapperPreservesFlush(t *testing.T) {
	t.Parallel()
	var sawFlusher bool
	handler := withLogging(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		sawFlusher = ok
		if !ok {
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: done\ndata: {}\n\n"))
		flusher.Flush()
	}))
	req := httptest.NewRequest(http.MethodPost, "/chat/stream", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if !sawFlusher {
		t.Fatal("logging wrapper hid http.Flusher, so /chat/stream would return an empty body")
	}
	if !strings.Contains(rec.Body.String(), "event: done") {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestChatRequiresToken(t *testing.T) {
	t.Parallel()
	body := strings.NewReader(`{"message":"who arrives today?","propertyId":12}`)
	req := httptest.NewRequest(http.MethodPost, "/chat", body)
	rec := httptest.NewRecorder()
	(&api{}).chat(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
}

func TestChatRequiresProperty(t *testing.T) {
	t.Parallel()
	body := strings.NewReader(`{"message":"who arrives today?"}`)
	req := httptest.NewRequest(http.MethodPost, "/chat", body)
	req.Header.Set("X-Token", "test-token")
	rec := httptest.NewRecorder()
	(&api{}).chat(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
}

func TestScopedRoutesRequireTokenAndProperty(t *testing.T) {
	t.Parallel()
	a := &api{}
	cases := []struct {
		name   string
		method string
		path   string
		body   string
		token  string
		want   int
	}{
		{name: "history no token", method: http.MethodGet, path: "/conversations/web-p1-abc", want: http.StatusUnauthorized},
		{name: "turns no token", method: http.MethodGet, path: "/analytics/turns", want: http.StatusUnauthorized},
		{name: "events no token", method: http.MethodGet, path: "/events", want: http.StatusUnauthorized},
		{name: "feedback no token", method: http.MethodPost, path: "/feedback", body: `{"turnId":"t","rating":"thumbs_up"}`, want: http.StatusUnauthorized},
		{name: "turns no property", method: http.MethodGet, path: "/analytics/turns", token: "test-token", want: http.StatusBadRequest},
		{name: "events no property", method: http.MethodGet, path: "/events", token: "test-token", want: http.StatusBadRequest},
		{name: "history no property", method: http.MethodGet, path: "/conversations/web-p1-abc", token: "test-token", want: http.StatusBadRequest},
		{name: "feedback no property", method: http.MethodPost, path: "/feedback", body: `{"turnId":"t","rating":"thumbs_up"}`, token: "test-token", want: http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var body *strings.Reader
			if tc.body == "" {
				body = strings.NewReader("")
			} else {
				body = strings.NewReader(tc.body)
			}
			req := httptest.NewRequest(tc.method, tc.path, body)
			if tc.token != "" {
				req.Header.Set("X-Token", tc.token)
			}
			req.SetPathValue("sessionId", "web-p1-abc")
			rec := httptest.NewRecorder()
			switch {
			case strings.HasPrefix(tc.path, "/conversations/"):
				a.history(rec, req)
			case tc.path == "/analytics/turns":
				a.turns(rec, req)
			case tc.path == "/events":
				a.events(rec, req)
			default:
				a.feedback(rec, req)
			}
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d, body %s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}
