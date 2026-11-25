package http

import (
	"bytes"
	"compress/gzip"
	_ "embed" // the vendored Scalar bundle
	"io"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
)

// The Scalar API reference is vendored: the page loads no script from a third
// party (AUDIT S-11). The file is dist/browser/standalone.js of
// @scalar/api-reference 1.75.0 as published to npm, gzipped; see
// scalar/README.md for its source and how to update it.
//
//go:embed scalar/api-reference-1.75.0.js.gz
var scalarBundleGzip []byte

const (
	scalarBundleName = "api-reference-1.75.0.js"
	scalarScriptPath = "/scalar/" + scalarBundleName
	// scalarBundleSHA256 is the SHA-256 (hex) of the uncompressed bundle; a
	// test checks the embedded file against it.
	scalarBundleSHA256 = "61145fca93f21c47384fa4533dfe4ddd9bb0a450482dfa873443f9fee37bc9e3"
)

// scalarContentSecurityPolicy replaces the default deny-all policy on the
// reference page: scripts only from this origin, data only from this origin,
// and no framing. Scalar injects its styles, so inline styles are allowed;
// inline scripts are not.
const scalarContentSecurityPolicy = "default-src 'none'; " +
	"script-src 'self'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; font-src 'self' data:; connect-src 'self'; " +
	"frame-ancestors 'none'; base-uri 'none'; form-action 'none'"

const scalarReferenceHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Machinarchy API Reference</title>
  <style>html, body, #app { margin: 0; min-height: 100%; }</style>
</head>
<body>
  <div id="app"></div>
  <script src="` + scalarScriptPath + `"></script>
  <script src="/scalar/init.js"></script>
</body>
</html>`

// scalarInitScript is served from this origin so the page needs no inline
// script.
const scalarInitScript = `Scalar.createApiReference('#app', {
  url: '/openapi.json',
  agent: { disabled: true },
  telemetry: false,
  withDefaultFonts: false,
  authentication: { preferredSecurityScheme: 'BearerAuth' }
});
`

func (s *Server) scalarReferenceHandler(c echo.Context) error {
	c.Response().Header().Set("Content-Security-Policy", scalarContentSecurityPolicy)
	return c.HTML(http.StatusOK, scalarReferenceHTML)
}

func (s *Server) scalarInitHandler(c echo.Context) error {
	return c.Blob(http.StatusOK, "text/javascript; charset=utf-8", []byte(scalarInitScript))
}

// scalarBundleHandler serves the vendored bundle. The file name carries the
// version, so it can be cached for good. Browsers that accept gzip get the
// embedded bytes as they are.
func (s *Server) scalarBundleHandler(c echo.Context) error {
	header := c.Response().Header()
	header.Set("Cache-Control", "public, max-age=31536000, immutable")
	header.Add("Vary", "Accept-Encoding")
	if strings.Contains(c.Request().Header.Get("Accept-Encoding"), "gzip") {
		header.Set("Content-Encoding", "gzip")
		return c.Blob(http.StatusOK, "text/javascript; charset=utf-8", scalarBundleGzip)
	}
	reader, err := gzip.NewReader(bytes.NewReader(scalarBundleGzip))
	if err != nil {
		return err
	}
	defer reader.Close()
	return c.Stream(http.StatusOK, "text/javascript; charset=utf-8", io.Reader(reader))
}
