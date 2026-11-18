package http

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

// scalarScriptURL is the only third-party script the API origin loads. The
// page's Content-Security-Policy allows exactly this versioned file, and the
// page is served only when API docs are enabled (AUDIT S-11).
const scalarScriptURL = "https://cdn.jsdelivr.net/npm/@scalar/api-reference@1.75.0"

// scalarContentSecurityPolicy replaces the default deny-all policy on the
// reference page: scripts only from this origin and the pinned Scalar file,
// data only from this origin, and no framing. Scalar injects its styles, so
// inline styles are allowed; inline scripts are not.
const scalarContentSecurityPolicy = "default-src 'none'; " +
	"script-src 'self' " + scalarScriptURL + "; " +
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
  <script src="` + scalarScriptURL + `" crossorigin="anonymous" referrerpolicy="no-referrer"></script>
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
