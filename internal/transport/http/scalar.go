package http

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

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
  <script src="https://cdn.jsdelivr.net/npm/@scalar/api-reference@1.75.0"></script>
  <script>
    Scalar.createApiReference('#app', {
      url: '/openapi.json',
      agent: { disabled: true },
      authentication: { preferredSecurityScheme: 'BearerAuth' }
    });
  </script>
</body>
</html>`

func (s *Server) scalarReferenceHandler(c echo.Context) error {
	return c.HTML(http.StatusOK, scalarReferenceHTML)
}
