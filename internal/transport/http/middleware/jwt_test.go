package middleware_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/transport/http/middleware"
)

var jwtSecret = []byte("jwt-test-secret-0123456789abcdef0123")

type roleStore struct {
	accessStoreStub
	role domain.Role
	err  error
}

func (s *roleStore) GetWorkspaceRole(context.Context, int, int) (domain.Role, error) {
	return s.role, s.err
}

func sign(t *testing.T, method jwt.SigningMethod, key any, claims domain.JWTCustomClaims) string {
	t.Helper()
	token, err := jwt.NewWithClaims(method, claims).SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func claims(mutate func(*domain.JWTCustomClaims)) domain.JWTCustomClaims {
	now := time.Now()
	c := domain.JWTCustomClaims{UserID: 7, WorkspaceID: 1, Username: "alice", RegisteredClaims: jwt.RegisteredClaims{
		Issuer: "machinarchy", IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour))}}
	if mutate != nil {
		mutate(&c)
	}
	return c
}

// TestJWTValidation covers the session-token checks: algorithm pinning,
// signature, issuer, expiry, maximum age and lifespan, required claims,
// current membership, and where the token may come from.
func TestJWTValidation(t *testing.T) {
	valid := sign(t, jwt.SigningMethodHS256, jwtSecret, claims(nil))
	old := time.Now().Add(-25 * time.Hour)
	tests := []struct {
		name   string
		token  string
		cookie bool
		store  *roleStore
		want   int
	}{
		{"valid bearer", valid, false, &roleStore{role: domain.RoleOperator}, http.StatusOK},
		{"valid cookie", valid, true, &roleStore{role: domain.RoleReader}, http.StatusOK},
		{"missing", "", false, &roleStore{role: domain.RoleOwner}, http.StatusUnauthorized},
		{"garbage", "not.a.jwt", false, &roleStore{role: domain.RoleOwner}, http.StatusUnauthorized},
		{"wrong secret", sign(t, jwt.SigningMethodHS256, []byte("another-secret-0123456789abcdef0123"), claims(nil)), false, &roleStore{role: domain.RoleOwner}, http.StatusUnauthorized},
		{"HS512 instead of HS256", sign(t, jwt.SigningMethodHS512, jwtSecret, claims(nil)), false, &roleStore{role: domain.RoleOwner}, http.StatusUnauthorized},
		{"alg none", sign(t, jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, claims(nil)), false, &roleStore{role: domain.RoleOwner}, http.StatusUnauthorized},
		{"wrong issuer", sign(t, jwt.SigningMethodHS256, jwtSecret, claims(func(c *domain.JWTCustomClaims) { c.Issuer = "evil" })), false, &roleStore{role: domain.RoleOwner}, http.StatusUnauthorized},
		{"expired", sign(t, jwt.SigningMethodHS256, jwtSecret, claims(func(c *domain.JWTCustomClaims) {
			c.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-2 * time.Minute))
		})), false, &roleStore{role: domain.RoleOwner}, http.StatusUnauthorized},
		{"no expiry", sign(t, jwt.SigningMethodHS256, jwtSecret, claims(func(c *domain.JWTCustomClaims) { c.ExpiresAt = nil })), false, &roleStore{role: domain.RoleOwner}, http.StatusUnauthorized},
		{"too old", sign(t, jwt.SigningMethodHS256, jwtSecret, claims(func(c *domain.JWTCustomClaims) {
			c.IssuedAt, c.ExpiresAt = jwt.NewNumericDate(old), jwt.NewNumericDate(time.Now().Add(time.Hour))
		})), false, &roleStore{role: domain.RoleOwner}, http.StatusUnauthorized},
		{"issued in the future", sign(t, jwt.SigningMethodHS256, jwtSecret, claims(func(c *domain.JWTCustomClaims) {
			c.IssuedAt = jwt.NewNumericDate(time.Now().Add(10 * time.Minute))
		})), false, &roleStore{role: domain.RoleOwner}, http.StatusUnauthorized},
		{"no user", sign(t, jwt.SigningMethodHS256, jwtSecret, claims(func(c *domain.JWTCustomClaims) { c.UserID = 0 })), false, &roleStore{role: domain.RoleOwner}, http.StatusUnauthorized},
		{"no longer a member", valid, false, &roleStore{err: domain.ErrMembershipNotFound}, http.StatusUnauthorized},
		{"store failure", valid, false, &roleStore{err: errors.New("db down")}, http.StatusInternalServerError},
		{"API token in a cookie", "mch_pat_abc", true, &roleStore{role: domain.RoleOwner}, http.StatusUnauthorized},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := echo.New()
			var principal domain.Principal
			handler := middleware.JWTAuth(jwtSecret, tc.store)(func(c echo.Context) error {
				principal, _ = middleware.Principal(c)
				return c.NoContent(http.StatusOK)
			})
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.token != "" {
				if tc.cookie {
					request.AddCookie(&http.Cookie{Name: "auth_token", Value: tc.token})
				} else {
					request.Header.Set("Authorization", "Bearer "+tc.token)
				}
			}
			recorder := httptest.NewRecorder()
			if err := handler(e.NewContext(request, recorder)); err != nil {
				t.Fatal(err)
			}
			if recorder.Code != tc.want {
				t.Fatalf("status = %d, want %d (%s)", recorder.Code, tc.want, recorder.Body)
			}
			if tc.want == http.StatusOK && (principal.UserID != 7 || principal.Role != tc.store.role) {
				t.Fatalf("principal = %+v; the role must come from the store, not the token", principal)
			}
		})
	}
}
