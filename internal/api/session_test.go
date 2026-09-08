package api_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/r4ph92/DevDuel/internal/api"
	"github.com/r4ph92/DevDuel/internal/auth"
)

func (h *harness) me(cookie *http.Cookie) response {
	h.t.Helper()
	req, err := http.NewRequestWithContext(h.t.Context(), http.MethodGet, h.srv.URL+"/me", nil)
	if err != nil {
		h.t.Fatal(err)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	return h.do(req)
}

func TestMeFollowsTheSessionLifecycle(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false)
	h.registerPlayer()
	login := h.post("/auth/login", loginBody)
	cookie := login.sessionCookie()
	if cookie == nil {
		t.Fatal("no login cookie")
	}
	got := h.me(cookie)
	if got.status != http.StatusOK {
		t.Fatalf("GET /me = %d: %s", got.status, got.body)
	}
	var expected, actual map[string]json.RawMessage
	login.decode(t, &expected)
	got.decode(t, &actual)
	if len(actual) != 1 || string(actual["user"]) != string(expected["user"]) {
		t.Fatalf("unexpected account: %s", got.body)
	}
	if got.header.Get("Cache-Control") != "no-store" || len(got.cookies) != 0 {
		t.Fatal("response cached or cookie changed")
	}
	var expiry time.Time
	if err := h.db.Pool().QueryRow(t.Context(), "select expires_at from sessions where token_hash=$1", auth.Digest(cookie.Value)).Scan(&expiry); err != nil {
		t.Fatal(err)
	}
	var loginExpiry time.Time
	if err := json.Unmarshal(expected["expires_at"], &loginExpiry); err != nil {
		t.Fatal(err)
	}
	if expiry.Sub(loginExpiry).Abs() > time.Microsecond {
		t.Fatal("session expiry changed")
	}
	creds, err := h.db.CredentialsByEmail(t.Context(), "player@example.test")
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{password, creds.Hash, cookie.Value, "argon2"} {
		if strings.Contains(string(got.body), secret) || strings.Contains(h.logs.String(), secret) {
			t.Fatal("secret leaked")
		}
	}
	h.post("/auth/logout", "", cookie)
	revoked := h.me(cookie)
	if revoked.status != http.StatusUnauthorized || len(revoked.cookies) != 0 {
		t.Fatalf("revoked session: %d", revoked.status)
	}
}

func TestMeRejectsInvalidSessionsIdentically(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false)
	h.registerPlayer()
	u, err := h.db.UserByEmail(t.Context(), "player@example.test")
	if err != nil {
		t.Fatal(err)
	}
	expired := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("e", 32)))
	if _, err := h.db.Pool().Exec(t.Context(), `insert into sessions (token_hash, user_id, created_at, expires_at)
		values ($1, $2, now() - interval '2 hours', now() - interval '1 hour')`, auth.Digest(expired), u.ID); err != nil {
		t.Fatal(err)
	}
	baseline := h.me(nil)
	for _, token := range []string{"", "malformed", strings.Repeat("!", 43), base64.RawURLEncoding.EncodeToString(make([]byte, 32)), expired} {
		res := h.me(&http.Cookie{Name: api.SessionCookie, Value: token})
		if res.status != http.StatusUnauthorized || string(res.body) != string(baseline.body) || res.failure(t).Code != "unauthenticated" {
			t.Fatalf("unexpected failure: %d %s", res.status, res.body)
		}
		if len(res.cookies) != 0 || res.header.Get("Cache-Control") != "no-store" {
			t.Fatal("cookie changed or failure cached")
		}
	}
}

func TestMeReturnsOnlyTheSessionOwner(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false)
	h.registerPlayer()
	first := h.post("/auth/login", loginBody).sessionCookie()
	res := h.post("/auth/register", `{"email":"other@example.test","username":"other","password":"correct horse battery staple"}`)
	if res.status != http.StatusCreated {
		t.Fatal(string(res.body))
	}
	second := h.post("/auth/login", `{"email":"other@example.test","password":"correct horse battery staple"}`).sessionCookie()
	for name, cookie := range map[string]*http.Cookie{"player": first, "other": second} {
		res := h.me(cookie)
		var body struct{ User struct{ Username string } }
		res.decode(t, &body)
		if res.status != http.StatusOK || body.User.Username != name {
			t.Fatalf("wrong session owner: %s", res.body)
		}
	}
}

func TestMeReportsDatabaseFailureAsInternal(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false)
	h.db.Close()
	res := h.me(&http.Cookie{Name: api.SessionCookie, Value: base64.RawURLEncoding.EncodeToString(make([]byte, 32))})
	if res.status != http.StatusInternalServerError || res.failure(t).Code != "internal" {
		t.Fatalf("database failure: %d %s", res.status, res.body)
	}
	if len(res.cookies) != 0 {
		t.Fatal("database failure cleared cookie")
	}
}

func TestUserFromContextIsAbsentWithoutAuthentication(t *testing.T) {
	if _, ok := api.UserFromContext(context.Background()); ok {
		t.Fatal("unauthenticated context has a user")
	}
}
