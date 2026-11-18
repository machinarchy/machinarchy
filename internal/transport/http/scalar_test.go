package http

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	custommw "github.com/machinarchy/machinarchy/internal/transport/http/middleware"
)

// The API reference is the only HTML the server returns. Its policy must allow
// exactly one third-party script and no inline script (AUDIT S-11).
func TestScalarReferencePolicy(t *testing.T) {
	s := &Server{}
	e := echo.New()
	e.Use(custommw.SecurityHeaders(true))
	e.GET("/scalar", s.scalarReferenceHandler)
	e.GET("/scalar/init.js", s.scalarInitHandler)

	recorder := httptest.NewRecorder()
	e.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/scalar", nil))
	policy := recorder.Header().Get("Content-Security-Policy")
	if policy != scalarContentSecurityPolicy {
		t.Fatalf("CSP = %q", policy)
	}
	for _, forbidden := range []string{"'unsafe-inline' 'self' https", "'unsafe-eval'", "script-src *", "https:;"} {
		if strings.Contains(policy, forbidden) {
			t.Errorf("policy contains %q", forbidden)
		}
	}
	scriptSrc := regexp.MustCompile(`script-src ([^;]*);`).FindStringSubmatch(policy)
	if len(scriptSrc) != 2 || scriptSrc[1] != "'self' "+scalarScriptURL {
		t.Fatalf("script-src = %q, want only self and the pinned Scalar file", scriptSrc)
	}
	if !strings.Contains(scalarScriptURL, "@") {
		t.Fatal("the Scalar script must be pinned to a version")
	}
	body := recorder.Body.String()
	if inline := regexp.MustCompile(`<script>`).FindString(body); inline != "" {
		t.Fatal("the reference page must not contain inline scripts")
	}
	if recorder.Header().Get("X-Frame-Options") != "DENY" || recorder.Header().Get("Strict-Transport-Security") == "" {
		t.Fatal("the reference page lost the common security headers")
	}

	recorder = httptest.NewRecorder()
	e.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/scalar/init.js", nil))
	if !strings.HasPrefix(recorder.Header().Get("Content-Type"), "text/javascript") ||
		!strings.Contains(recorder.Body.String(), "withDefaultFonts: false") {
		t.Fatalf("init script = %q %q", recorder.Header().Get("Content-Type"), recorder.Body.String())
	}
}
