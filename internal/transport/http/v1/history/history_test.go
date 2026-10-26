package history

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/machinarchy/machinarchy/internal/metrics"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
)

func TestHistoryHandler(t *testing.T) {
	// Set test database path to a temp file
	os.Setenv("HOSTINFO_DB_PATH", t.TempDir()+"/test.db")
	metrics.InitDB()

	// Seed a test point so it's never empty
	metrics.SaveMetricPoint(42.5, 65.0)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/history", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	// Call History handler
	if assert.NoError(t, History(c)) {
		assert.Equal(t, http.StatusOK, rec.Code)

		var points []metrics.DataPoint
		err := json.Unmarshal(rec.Body.Bytes(), &points)
		assert.NoError(t, err)

		assert.NotEmpty(t, points)
		assert.Equal(t, 42.5, points[0].CPUPercent)
	}
}
