package history

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/machinarchy/machinarchy/internal/domain"
)

type metricsServiceStub struct {
	points []domain.DataPoint
	err    error
}

func (s metricsServiceStub) GetHistory(context.Context, int, string) ([]domain.DataPoint, error) {
	return s.points, s.err
}

func (metricsServiceStub) GetLiveDiagnostics(context.Context, int, int) (*domain.HostRuntimeDiagnostics, error) {
	return nil, errors.New("not implemented")
}

func TestHistoryHandler(t *testing.T) {
	tests := []struct {
		name       string
		query      string
		serviceErr error
		wantStatus int
		wantPoints int
	}{
		{
			name:       "returns stored history",
			wantStatus: http.StatusOK,
			wantPoints: 1,
		},
		{
			name:       "rejects unsupported range",
			query:      "?range=invalid",
			serviceErr: domain.ErrInvalidInput,
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := echo.New()
			req := httptest.NewRequest(http.MethodGet, "/history"+tt.query, nil)
			rec := httptest.NewRecorder()
			ctx := e.NewContext(req, rec)
			ctx.Set("principal", domain.Principal{UserID: 1, WorkspaceID: 1, Role: domain.RoleOwner})
			point := domain.DataPoint{CPUPercent: 42.5, MemPercent: 65.0}
			handler := NewHandler(metricsServiceStub{
				points: []domain.DataPoint{point},
				err:    tt.serviceErr,
			}, nil)

			if err := handler.GetHistory(ctx); err != nil {
				t.Fatalf("GetHistory() error = %v", err)
			}
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.wantStatus != http.StatusOK {
				return
			}

			var points []domain.DataPoint
			if err := json.Unmarshal(rec.Body.Bytes(), &points); err != nil {
				t.Fatalf("decode history response: %v", err)
			}
			if len(points) != tt.wantPoints {
				t.Fatalf("got %d data points, want %d", len(points), tt.wantPoints)
			}
			if points[0].CPUPercent != point.CPUPercent {
				t.Errorf("CPUPercent = %v, want %v", points[0].CPUPercent, point.CPUPercent)
			}
		})
	}
}
