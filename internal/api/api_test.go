package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/r4ph92/DevDuel/internal/api"
	"github.com/r4ph92/DevDuel/internal/auth"
	"github.com/r4ph92/DevDuel/internal/store"
	"github.com/r4ph92/DevDuel/internal/store/storetest"
)

// harness is an API in front of a database of this test's own, with its log
// captured so that a test can assert on what was not written to it.
type harness struct {
	t    *testing.T
	srv  *httptest.Server
	db   *store.Store
	logs *syncBuffer
}

// syncBuffer collects log output. The server writes from its own goroutines,
// so the buffer has to be safe for that.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newHarness(t *testing.T, secureCookies bool) *harness {
	t.Helper()

	db := storetest.New(t)
	logs := &syncBuffer{}

	srv := httptest.NewServer(api.New(api.Config{
		Auth:          auth.NewService(db),
		Logger:        slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		SecureCookies: secureCookies,
	}))
	t.Cleanup(srv.Close)

	return &harness{t: t, srv: srv, db: db, logs: logs}
}

// response is a reply, kept as bytes so a test can look for things that
// should not be in it.
type response struct {
	status  int
	body    []byte
	cookies []*http.Cookie
	header  http.Header
}

// decode reads the body into v.
func (r response) decode(t *testing.T, v any) {
	t.Helper()

	if err := json.Unmarshal(r.body, v); err != nil {
		t.Fatalf("decode %q: %v", r.body, err)
	}
}

// failure is the error shape every failing response uses.
func (r response) failure(t *testing.T) struct{ Code, Message string } {
	t.Helper()

	var body struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	r.decode(t, &body)
	return body.Error
}

// post sends a request with an optional session cookie.
func (h *harness) post(path, body string, cookies ...*http.Cookie) response {
	h.t.Helper()

	req, err := http.NewRequestWithContext(h.t.Context(), http.MethodPost, h.srv.URL+path, strings.NewReader(body))
	if err != nil {
		h.t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	return h.do(req)
}

func (h *harness) get(path string) response {
	h.t.Helper()

	req, err := http.NewRequestWithContext(h.t.Context(), http.MethodGet, h.srv.URL+path, nil)
	if err != nil {
		h.t.Fatalf("build request: %v", err)
	}
	return h.do(req)
}

func (h *harness) do(req *http.Request) response {
	h.t.Helper()

	res, err := h.srv.Client().Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", req.Method, req.URL.Path, err)
	}
	defer func() { _ = res.Body.Close() }()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		h.t.Fatalf("read body: %v", err)
	}
	return response{status: res.StatusCode, body: body, cookies: res.Cookies(), header: res.Header}
}

// sessionCookie is the session the response set, or nil.
func (r response) sessionCookie() *http.Cookie {
	for _, c := range r.cookies {
		if c.Name == api.SessionCookie {
			return c
		}
	}
	return nil
}

const (
	password     = "correct horse battery staple"
	registerBody = `{"email":"player@example.test","username":"player","password":"correct horse battery staple"}`
	loginBody    = `{"email":"player@example.test","password":"correct horse battery staple"}`
)

func (h *harness) registerPlayer() response {
	h.t.Helper()

	res := h.post("/auth/register", registerBody)
	if res.status != http.StatusCreated {
		h.t.Fatalf("register: status %d, body %s", res.status, res.body)
	}
	return res
}

func TestHealthAnswersWithoutADatabase(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false)

	res := h.get("/health")
	if res.status != http.StatusOK {
		t.Errorf("status = %d, want 200", res.status)
	}
}
