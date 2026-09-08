package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/r4ph92/DevDuel/internal/auth"
)

func TestRequireUserStopsBeforeTheHandler(t *testing.T) {
	s := &server{auth: &auth.Service{}, log: slog.Default()}
	for _, token := range []string{"", "malformed"} {
		called := false
		handler := s.requireUser(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
		req := httptest.NewRequest(http.MethodGet, "/protected", nil)
		if token != "" {
			req.AddCookie(&http.Cookie{Name: SessionCookie, Value: token})
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if called || res.Code != http.StatusUnauthorized {
			t.Fatalf("handler called=%t, status=%d", called, res.Code)
		}
	}
}
