package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/machinarchy/machinarchy/internal/domain"
)

func TestSignUpDisabled(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/signup", nil)
	recorder := httptest.NewRecorder()
	ctx := e.NewContext(req, recorder)

	handler := NewHandler(nil, false, false)
	if err := handler.SignUp(ctx); err != nil {
		t.Fatalf("SignUp() error = %v", err)
	}

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
}

type fakeAuth struct {
	signUp    func(domain.AuthSignUpRequest) (*domain.AuthTokenResponse, error)
	login     func(domain.AuthLoginRequest) (*domain.AuthTokenResponse, error)
	verify    func(string) (*domain.UserResponse, error)
	signedUps int
	loggedOut []string
}

func (f *fakeAuth) SignUp(_ context.Context, r domain.AuthSignUpRequest) (*domain.AuthTokenResponse, error) {
	f.signedUps++
	return f.signUp(r)
}
func (f *fakeAuth) Login(_ context.Context, r domain.AuthLoginRequest) (*domain.AuthTokenResponse, error) {
	return f.login(r)
}
func (f *fakeAuth) VerifySession(_ context.Context, u string) (*domain.UserResponse, error) {
	return f.verify(u)
}
func (f *fakeAuth) Logout(_ context.Context, token string) error {
	f.loggedOut = append(f.loggedOut, token)
	return nil
}
func (f *fakeAuth) ListSessions(context.Context, domain.Principal) ([]domain.Session, error) {
	return nil, nil
}
func (f *fakeAuth) RevokeSession(context.Context, domain.Principal, string) error { return nil }
func (f *fakeAuth) RevokeAllSessions(context.Context, domain.Principal) (int, error) {
	return 0, nil
}
func (f *fakeAuth) ChangePassword(context.Context, domain.Principal, string, string) error {
	return nil
}

func serve(h *Handler, method, path, body string, username string) *httptest.ResponseRecorder {
	e := echo.New()
	RegisterRoutes(e.Group("/api"), h)
	e.GET("/api/v1/me", h.GetMe, func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if username != "" {
				c.Set("username", username)
			}
			return next(c)
		}
	})
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	e.ServeHTTP(recorder, request)
	return recorder
}

func sessionCookie(t *testing.T, recorder *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == "auth_token" {
			return cookie
		}
	}
	return nil
}

func TestLoginSetsAHardenedSessionCookie(t *testing.T) {
	auth := &fakeAuth{login: func(r domain.AuthLoginRequest) (*domain.AuthTokenResponse, error) {
		if r.Password != "right-password" {
			return nil, domain.ErrInvalidCredentials
		}
		return &domain.AuthTokenResponse{Token: "session-token", User: domain.UserResponse{ID: 1, Username: r.Username}}, nil
	}}
	for _, secure := range []bool{true, false} {
		recorder := serve(NewHandler(auth, secure, false), http.MethodPost, "/api/auth/login", `{"username":"alice","password":"right-password"}`, "")
		cookie := sessionCookie(t, recorder)
		switch {
		case recorder.Code != http.StatusOK || cookie == nil:
			t.Fatalf("secure=%v: login = %d, cookie %v", secure, recorder.Code, cookie)
		case cookie.Value != "session-token" || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/":
			t.Fatalf("secure=%v: cookie = %+v", secure, cookie)
		case cookie.Secure != secure:
			t.Fatalf("cookie Secure = %v, want %v (it follows production mode)", cookie.Secure, secure)
		}
	}

	handler := NewHandler(auth, true, false)
	for name, tc := range map[string]struct {
		body string
		want int
	}{
		"wrong password": {`{"username":"alice","password":"wrong"}`, http.StatusUnauthorized},
		"no password":    {`{"username":"alice"}`, http.StatusBadRequest},
		"no identity":    {`{"password":"right-password"}`, http.StatusBadRequest},
		"not JSON":       {`{`, http.StatusBadRequest},
	} {
		recorder := serve(handler, http.MethodPost, "/api/auth/login", tc.body, "")
		if recorder.Code != tc.want || sessionCookie(t, recorder) != nil {
			t.Errorf("%s: %d (cookie %v), want %d and no cookie", name, recorder.Code, sessionCookie(t, recorder), tc.want)
		}
	}
	failing := &fakeAuth{login: func(domain.AuthLoginRequest) (*domain.AuthTokenResponse, error) { return nil, errors.New("db down") }}
	if recorder := serve(NewHandler(failing, true, false), http.MethodPost, "/api/auth/login", `{"username":"a","password":"p"}`, ""); recorder.Code != http.StatusInternalServerError ||
		strings.Contains(recorder.Body.String(), "db down") {
		t.Fatalf("store failure = %d %s (it must not leak the cause)", recorder.Code, recorder.Body)
	}
}

func TestSignUpFollowsTheLiveSetting(t *testing.T) {
	auth := &fakeAuth{signUp: func(r domain.AuthSignUpRequest) (*domain.AuthTokenResponse, error) {
		if r.Username == "taken" {
			return nil, domain.ErrUserAlreadyExists
		}
		return &domain.AuthTokenResponse{Token: "new-session"}, nil
	}}
	enabled := false
	handler := NewHandler(auth, true, true)                          // static setting on...
	handler.SetSignupEnabledProvider(func() bool { return enabled }) // ...but runtime configuration decides
	body := `{"username":"bob","password":"long-enough-password"}`
	if recorder := serve(handler, http.MethodPost, "/api/auth/signup", body, ""); recorder.Code != http.StatusForbidden || auth.signedUps != 0 {
		t.Fatalf("signup while disabled at runtime = %d (%d calls)", recorder.Code, auth.signedUps)
	}
	enabled = true
	recorder := serve(handler, http.MethodPost, "/api/auth/signup", body, "")
	if cookie := sessionCookie(t, recorder); recorder.Code != http.StatusCreated || cookie == nil || !cookie.Secure || !cookie.HttpOnly {
		t.Fatalf("signup = %d, cookie %+v", recorder.Code, cookie)
	}
	for name, tc := range map[string]struct {
		body string
		want int
	}{
		"taken":          {`{"username":"taken","password":"long-enough-password"}`, http.StatusConflict},
		"short password": {`{"username":"bob","password":"short"}`, http.StatusBadRequest},
		"long password":  {`{"username":"bob","password":"` + strings.Repeat("p", 73) + `"}`, http.StatusBadRequest},
		"no username":    {`{"password":"long-enough-password"}`, http.StatusBadRequest},
	} {
		if recorder := serve(handler, http.MethodPost, "/api/auth/signup", tc.body, ""); recorder.Code != tc.want {
			t.Errorf("%s: %d, want %d", name, recorder.Code, tc.want)
		}
	}
}

func TestLogoutClearsTheCookie(t *testing.T) {
	recorder := serve(NewHandler(&fakeAuth{}, true, false), http.MethodPost, "/api/auth/logout", "", "")
	cookie := sessionCookie(t, recorder)
	if recorder.Code != http.StatusOK || cookie == nil || cookie.Value != "" || cookie.MaxAge >= 0 || !cookie.HttpOnly || !cookie.Secure {
		t.Fatalf("logout = %d, cookie %+v", recorder.Code, cookie)
	}
}

func TestGetMe(t *testing.T) {
	auth := &fakeAuth{verify: func(username string) (*domain.UserResponse, error) {
		switch username {
		case "alice":
			return &domain.UserResponse{ID: 1, Username: "alice"}, nil
		case "gone":
			return nil, domain.ErrUserNotFound
		}
		return nil, errors.New("db down")
	}}
	handler := NewHandler(auth, true, false)
	if recorder := serve(handler, http.MethodGet, "/api/v1/me", "", "alice"); recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"alice"`) {
		t.Fatalf("me = %d %s", recorder.Code, recorder.Body)
	}
	for name, tc := range map[string]struct {
		username string
		want     int
	}{
		"no session":      {"", http.StatusUnauthorized},
		"deleted account": {"gone", http.StatusUnauthorized},
		"store failure":   {"broken", http.StatusInternalServerError},
	} {
		if recorder := serve(handler, http.MethodGet, "/api/v1/me", "", tc.username); recorder.Code != tc.want {
			t.Errorf("%s: %d, want %d", name, recorder.Code, tc.want)
		}
	}
}

// S-08: logout hands the session token to the service, which revokes the
// session on the server; an API token is never treated as a session.
func TestLogoutRevokesTheSessionOfTheTokenSent(t *testing.T) {
	auth := &fakeAuth{}
	h := NewHandler(auth, false, false)
	e := echo.New()
	RegisterRoutes(e.Group("/api"), h)
	for _, tc := range []struct {
		name, bearer, cookie, want string
	}{
		{"bearer", "session.jwt.value", "", "session.jwt.value"},
		{"cookie", "", "cookie.jwt.value", "cookie.jwt.value"},
		{"bearer wins", "bearer.jwt", "cookie.jwt", "bearer.jwt"},
		{"API token ignored", "mch_pat_secret", "", ""},
		{"nothing", "", "", ""},
	} {
		auth.loggedOut = nil
		request := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
		if tc.bearer != "" {
			request.Header.Set("Authorization", "Bearer "+tc.bearer)
		}
		if tc.cookie != "" {
			request.AddCookie(&http.Cookie{Name: "auth_token", Value: tc.cookie})
		}
		recorder := httptest.NewRecorder()
		e.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK || len(auth.loggedOut) != 1 || auth.loggedOut[0] != tc.want {
			t.Errorf("%s: status %d, service got %q, want %q", tc.name, recorder.Code, auth.loggedOut, tc.want)
		}
	}
}

type fakeTokens struct{ tokens []domain.APIToken }

func (f fakeTokens) ListAPITokens(context.Context, int, int) ([]domain.APIToken, error) {
	return f.tokens, nil
}

func serveMe(h *Handler, principal *domain.Principal) *httptest.ResponseRecorder {
	e := echo.New()
	e.GET("/api/v1/me", h.GetMe, func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set("username", "alice")
			if principal != nil {
				c.Set("principal", *principal)
			}
			return next(c)
		}
	})
	recorder := httptest.NewRecorder()
	e.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/me", nil))
	return recorder
}

// C-03: /me tells a session from a token, gives the role, and for a token its
// name and expiry; the token's secret and hash are never in the answer.
func TestGetMeReportsRoleAndTokenDetails(t *testing.T) {
	expires := time.Date(2031, 5, 6, 7, 8, 0, 0, time.UTC)
	auth := &fakeAuth{verify: func(string) (*domain.UserResponse, error) { return &domain.UserResponse{ID: 1, Username: "alice"}, nil }}
	handler := NewHandler(auth, true, false)
	handler.SetTokenLookup(fakeTokens{tokens: []domain.APIToken{
		{ID: 3, Name: "other", Role: domain.RoleReader},
		{ID: 7, Name: "ci-deploy", Role: domain.RoleOperator, TokenHash: "deadbeef-hash", ExpiresAt: &expires},
	}})

	session := serveMe(handler, &domain.Principal{UserID: 1, WorkspaceID: 1, Role: domain.RoleOwner, SessionID: "s1"})
	if session.Code != http.StatusOK || !strings.Contains(session.Body.String(), `"role":"owner"`) || !strings.Contains(session.Body.String(), `"authMethod":"session"`) ||
		strings.Contains(session.Body.String(), "tokenName") || strings.Contains(session.Body.String(), "expiresAt") {
		t.Fatalf("session me = %d %s", session.Code, session.Body)
	}
	token := serveMe(handler, &domain.Principal{UserID: 1, WorkspaceID: 1, Role: domain.RoleOperator, APITokenID: 7})
	for _, want := range []string{`"role":"operator"`, `"authMethod":"api_token"`, `"tokenId":7`, `"tokenName":"ci-deploy"`, `"expiresAt":"2031-05-06T07:08:00Z"`} {
		if !strings.Contains(token.Body.String(), want) {
			t.Fatalf("token me lacks %s: %s", want, token.Body)
		}
	}
	if strings.Contains(token.Body.String(), "deadbeef") || strings.Contains(token.Body.String(), "other") {
		t.Fatalf("token me leaks: %s", token.Body)
	}
	// Without a lookup the token is still identified, without name or expiry.
	bare := serveMe(NewHandler(auth, true, false), &domain.Principal{UserID: 1, WorkspaceID: 1, Role: domain.RoleOperator, APITokenID: 7})
	if !strings.Contains(bare.Body.String(), `"authMethod":"api_token"`) || strings.Contains(bare.Body.String(), "tokenName") {
		t.Fatalf("bare token me = %s", bare.Body)
	}
}
