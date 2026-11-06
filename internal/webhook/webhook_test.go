package webhook

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandler(t *testing.T) {
	var got []string
	h := Handler("s3cret-token-0123456789", func(name, text, key string) error {
		switch name {
		case "door":
			got = append(got, text+"|"+key)
			return nil
		case "starting":
			return errors.New("starting up")
		}
		return ErrUnknownHook
	})
	for _, tc := range []struct {
		name, path, auth, ctype, body string
		code                          int
		want                          string // in the response
	}{
		{"json key", "/hooks/door", "Bearer s3cret-token-0123456789", "application/json", `{"text": "person", "key": "front_door"}`, 202, "queued"},
		{"plain key", "/hooks/door?key=garden", "Bearer s3cret-token-0123456789", "text/plain", "cat", 202, "queued"},
		{"json", "/hooks/door", "Bearer s3cret-token-0123456789", "application/json", `{"text": "someone at the door"}`, 202, "queued"},
		{"json message", "/hooks/door", "Bearer s3cret-token-0123456789", "application/json; charset=utf-8", `{"message": "a car"}`, 202, "queued"},
		{"plain", "/hooks/door", "Bearer s3cret-token-0123456789", "text/plain", "dog in the garden", 202, "queued"},
		{"no token", "/hooks/door", "", "text/plain", "x", 401, "unauthorized"},
		{"wrong token", "/hooks/door", "Bearer nope", "text/plain", "x", 401, "unauthorized"},
		{"unknown hook", "/hooks/nope", "Bearer s3cret-token-0123456789", "text/plain", "x", 404, "no hook named nope"},
		{"not ready", "/hooks/starting", "Bearer s3cret-token-0123456789", "text/plain", "x", 503, "starting up"},
		{"empty", "/hooks/door", "Bearer s3cret-token-0123456789", "application/json", `{"text": "  "}`, 400, "empty event"},
		{"bad json", "/hooks/door", "Bearer s3cret-token-0123456789", "application/json", `{"text":`, 400, "invalid JSON"},
		{"too big", "/hooks/door", "Bearer s3cret-token-0123456789", "text/plain", strings.Repeat("x", MaxBody+1), 413, "too large"},
	} {
		req := httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body))
		if tc.auth != "" {
			req.Header.Set("Authorization", tc.auth)
		}
		req.Header.Set("Content-Type", tc.ctype)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tc.code || !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("%s: %d %s", tc.name, rec.Code, rec.Body.String())
		}
	}
	if strings.Join(got, ",") != "person|front_door,cat|garden,someone at the door|,a car|,dog in the garden|" {
		t.Errorf("delivered: %q", got)
	}

	// GET isn't a hook.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/hooks/door", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: %d", rec.Code)
	}

	// With no token configured, nothing gets in.
	open := Handler("", func(string, string, string) error { t.Fatal("delivered without a token"); return nil })
	req := httptest.NewRequest("POST", "/hooks/door", strings.NewReader("x"))
	req.Header.Set("Authorization", "Bearer ")
	rec = httptest.NewRecorder()
	open.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Errorf("no token configured: %d", rec.Code)
	}
}
