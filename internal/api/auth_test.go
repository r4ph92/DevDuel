package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/r4ph92/DevDuel/internal/auth"
)

func TestRegisterOpensAnAccount(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false)

	res := h.registerPlayer()

	var body struct {
		User struct {
			ID, Username, Email string
		} `json:"user"`
	}
	res.decode(t, &body)

	if body.User.Username != "player" {
		t.Errorf("username = %q, want player", body.User.Username)
	}
	if body.User.ID == "" {
		t.Error("the response carries no id")
	}
	// Registering does not sign anybody in: that is what login is for.
	if c := res.sessionCookie(); c != nil {
		t.Errorf("register set a session cookie %q", c.Value)
	}
}

// The collision is real and the answer must not say which half of it
// happened, or the form becomes a way of asking who has an account. Byte for
// byte the same response is the only version of that claim worth testing.
func TestRegisterWillNotSayWhichDetailWasTaken(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false)
	h.registerPlayer()

	email := h.post("/auth/register",
		`{"email":"player@example.test","username":"different","password":"correct horse battery staple"}`)
	username := h.post("/auth/register",
		`{"email":"different@example.test","username":"player","password":"correct horse battery staple"}`)

	if email.status != http.StatusConflict {
		t.Fatalf("a taken email = %d, want 409: %s", email.status, email.body)
	}
	if username.status != http.StatusConflict {
		t.Fatalf("a taken username = %d, want 409: %s", username.status, username.body)
	}
	if string(email.body) != string(username.body) {
		t.Errorf("the two collisions answer differently:\n %s\n %s", email.body, username.body)
	}
	if got := email.failure(t).Code; got != "taken" {
		t.Errorf("code = %q, want taken", got)
	}
}

func TestRegisterChecksWhatItWasGiven(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false)

	for name, tc := range map[string]struct {
		body string
		code string
	}{
		"not an address":    {`{"email":"player","username":"player","password":"correct horse battery"}`, "invalid_email"},
		"no username":       {`{"email":"a@b.test","username":"","password":"correct horse battery"}`, "invalid_username"},
		"a spaced username": {`{"email":"a@b.test","username":"two words","password":"correct horse battery"}`, "invalid_username"},
		"a short password":  {`{"email":"a@b.test","username":"player","password":"short"}`, "password_too_short"},
		"not json":          {`{`, "bad_request"},
		"an unknown field":  {`{"email":"a@b.test","username":"player","password":"correct horse","admin":true}`, "bad_request"},
		"two objects":       {registerBody + registerBody, "bad_request"},
	} {
		t.Run(name, func(t *testing.T) {
			res := h.post("/auth/register", tc.body)
			if res.status != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", res.status, res.body)
			}
			if got := res.failure(t).Code; got != tc.code {
				t.Errorf("code = %q, want %q", got, tc.code)
			}
		})
	}
}

func TestLoginSetsASessionThatFindsItsUser(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false)
	h.registerPlayer()

	res := h.post("/auth/login", loginBody)
	if res.status != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", res.status, res.body)
	}

	cookie := res.sessionCookie()
	if cookie == nil {
		t.Fatal("login set no session cookie")
	}
	if cookie.Value == "" {
		t.Fatal("the session cookie is empty")
	}
	if !cookie.HttpOnly {
		t.Error("the session cookie is readable by scripts, want HttpOnly")
	}
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax", cookie.SameSite)
	}
	if cookie.Secure {
		t.Error("the cookie is https only with secure cookies turned off")
	}

	user, err := h.db.SessionUser(t.Context(), auth.Digest(cookie.Value))
	if err != nil {
		t.Fatalf("the cookie does not name a session: %v", err)
	}
	if user.Username != "player" {
		t.Errorf("the session belongs to %q, want player", user.Username)
	}
}

func TestSecureCookiesMarkTheSessionHTTPSOnly(t *testing.T) {
	t.Parallel()
	h := newHarness(t, true)
	h.registerPlayer()

	res := h.post("/auth/login", loginBody)
	cookie := res.sessionCookie()
	if cookie == nil {
		t.Fatal("login set no session cookie")
	}
	if !cookie.Secure {
		t.Error("the session cookie is not marked Secure")
	}
}

// Byte for byte the same answer, so that nothing about the response says
// whether the account exists.
func TestAFailedLoginLooksTheSameEitherWay(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false)
	h.registerPlayer()

	wrong := h.post("/auth/login", `{"email":"player@example.test","password":"not the password"}`)
	unknown := h.post("/auth/login", `{"email":"nobody@example.test","password":"correct horse battery staple"}`)

	if wrong.status != http.StatusUnauthorized {
		t.Errorf("a wrong password = %d, want 401", wrong.status)
	}
	if unknown.status != http.StatusUnauthorized {
		t.Errorf("an unknown address = %d, want 401", unknown.status)
	}
	if string(wrong.body) != string(unknown.body) {
		t.Errorf("the two failures answer differently:\n %s\n %s", wrong.body, unknown.body)
	}
	if wrong.sessionCookie() != nil || unknown.sessionCookie() != nil {
		t.Error("a failed login set a session cookie")
	}
}

func TestLogoutEndsTheSessionAndClearsTheCookie(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false)
	h.registerPlayer()

	cookie := h.post("/auth/login", loginBody).sessionCookie()
	if cookie == nil {
		t.Fatal("login set no session cookie")
	}

	res := h.post("/auth/logout", "", cookie)
	if res.status != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", res.status, res.body)
	}
	if cleared := res.sessionCookie(); cleared == nil || cleared.MaxAge >= 0 {
		t.Errorf("logout did not clear the cookie: %+v", cleared)
	}

	if _, err := h.db.SessionUser(t.Context(), auth.Digest(cookie.Value)); err == nil {
		t.Error("the session still resolves after logging out")
	}

	// A stale tab signing out again is not an error.
	if again := h.post("/auth/logout", "", cookie); again.status != http.StatusNoContent {
		t.Errorf("logging out twice = %d, want 204", again.status)
	}
	if none := h.post("/auth/logout", ""); none.status != http.StatusNoContent {
		t.Errorf("logging out with no session = %d, want 204", none.status)
	}
}

// The issue this closes is done when password hashes never appear in logs or
// API responses. These two tests are that sentence.
func TestNoResponseCarriesThePasswordOrItsHash(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false)

	responses := []response{
		h.registerPlayer(),
		h.post("/auth/login", loginBody),
		h.post("/auth/login", `{"email":"player@example.test","password":"wrong"}`),
		h.post("/auth/register", registerBody),
	}

	creds, err := h.db.CredentialsByEmail(t.Context(), "player@example.test")
	if err != nil {
		t.Fatalf("CredentialsByEmail: %v", err)
	}

	for i, res := range responses {
		body := string(res.body)
		if strings.Contains(body, password) {
			t.Errorf("response %d carries the password: %s", i, body)
		}
		if strings.Contains(body, creds.Hash) {
			t.Errorf("response %d carries the stored hash: %s", i, body)
		}
		if strings.Contains(body, "argon2") {
			t.Errorf("response %d carries something hash shaped: %s", i, body)
		}
	}
}

func TestNothingLoggedCarriesThePasswordOrItsHash(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false)

	h.registerPlayer()
	cookie := h.post("/auth/login", loginBody).sessionCookie()
	h.post("/auth/login", `{"email":"player@example.test","password":"wrong"}`)
	h.post("/auth/register", registerBody)
	h.post("/auth/logout", "", cookie)

	creds, err := h.db.CredentialsByEmail(t.Context(), "player@example.test")
	if err != nil {
		t.Fatalf("CredentialsByEmail: %v", err)
	}

	logs := h.logs.String()
	if logs == "" {
		t.Fatal("nothing was logged at all, so this proves nothing")
	}
	for name, secret := range map[string]string{
		"the password":      password,
		"the stored hash":   creds.Hash,
		"the session token": cookie.Value,
	} {
		if strings.Contains(logs, secret) {
			t.Errorf("the log carries %s:\n%s", name, logs)
		}
	}
	// And the shape of a hash, in case one arrives by a route the test did
	// not think of.
	if strings.Contains(logs, "argon2") {
		t.Errorf("the log carries something hash shaped:\n%s", logs)
	}
}

func TestARouteAnswersOnlyTheMethodItIsFor(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false)

	if res := h.get("/auth/login"); res.status != http.StatusMethodNotAllowed {
		t.Errorf("GET /auth/login = %d, want 405", res.status)
	}
	if res := h.get("/nothing/here"); res.status != http.StatusNotFound {
		t.Errorf("GET /nothing/here = %d, want 404", res.status)
	}
}

// A body large enough to be a weapon is refused rather than read.
func TestAnEnormousBodyIsRefused(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false)

	huge := `{"email":"a@b.test","username":"player","password":"` + strings.Repeat("a", 1<<20) + `"}`
	res := h.post("/auth/register", huge)
	if res.status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", res.status)
	}
}
