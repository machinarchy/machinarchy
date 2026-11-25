package http

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	custommw "github.com/machinarchy/machinarchy/internal/transport/http/middleware"
)

// The API reference is the only HTML the server returns. It loads its script
// from this origin only and has no inline script (AUDIT S-11).
func TestScalarReferencePolicy(t *testing.T) {
	s := &Server{}
	e := echo.New()
	e.Use(custommw.SecurityHeaders(true))
	e.GET("/scalar", s.scalarReferenceHandler)
	e.GET("/scalar/init.js", s.scalarInitHandler)
	e.GET(scalarScriptPath, s.scalarBundleHandler)

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
	if len(scriptSrc) != 2 || scriptSrc[1] != "'self'" {
		t.Fatalf("script-src = %q, want only 'self'", scriptSrc)
	}
	body := recorder.Body.String()
	if external := regexp.MustCompile(`(?i)(src|href)="(https?:)?//`).FindString(body); external != "" || strings.Contains(policy, "http") {
		t.Fatalf("the reference page loads from another origin: %q", external)
	}
	if !strings.Contains(body, `src="`+scalarScriptPath+`"`) {
		t.Fatalf("the page does not load the vendored bundle: %s", body)
	}
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

// The vendored bundle is exactly the published file, whatever the client
// accepts, and is cacheable for good.
func TestScalarBundleIsThePublishedFile(t *testing.T) {
	s := &Server{}
	e := echo.New()
	e.GET(scalarScriptPath, s.scalarBundleHandler)

	for name, acceptEncoding := range map[string]string{"gzip client": "gzip, deflate", "plain client": ""} {
		request := httptest.NewRequest(http.MethodGet, scalarScriptPath, nil)
		if acceptEncoding != "" {
			request.Header.Set("Accept-Encoding", acceptEncoding)
		}
		recorder := httptest.NewRecorder()
		e.ServeHTTP(recorder, request)
		body := recorder.Body.Bytes()
		if (recorder.Header().Get("Content-Encoding") == "gzip") != (acceptEncoding != "") {
			t.Fatalf("%s: Content-Encoding = %q", name, recorder.Header().Get("Content-Encoding"))
		}
		if recorder.Header().Get("Content-Encoding") == "gzip" {
			reader, err := gzip.NewReader(bytes.NewReader(body))
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if body, err = io.ReadAll(reader); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		}
		sum := sha256.Sum256(body)
		if hex.EncodeToString(sum[:]) != scalarBundleSHA256 || len(body) != 4620361 {
			t.Fatalf("%s: bundle sha256 %x, %d bytes; want the published %s", name, sum, len(body), scalarBundleSHA256)
		}
		if got := recorder.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
			t.Fatalf("%s: Cache-Control = %q", name, got)
		}
		if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/javascript") {
			t.Fatalf("%s: Content-Type = %q", name, got)
		}
	}
}
