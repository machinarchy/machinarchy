package middleware

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"go.opentelemetry.io/otel/trace"
)

func Logging() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			start := time.Now()
			err := next(c)
			status := c.Response().Status
			if err != nil && status < http.StatusBadRequest {
				status = http.StatusInternalServerError
				var httpErr *echo.HTTPError
				if errors.As(err, &httpErr) {
					status = httpErr.Code
				}
			} else if status == 0 {
				status = http.StatusOK
			}
			route := c.Path()
			if route == "" {
				route = "unmatched"
			}
			attrs := []slog.Attr{
				slog.String("request_id", c.Response().Header().Get(echo.HeaderXRequestID)),
				slog.String("method", c.Request().Method),
				slog.String("route", route),
				slog.Int("status", status),
				slog.Int64("duration_ms", time.Since(start).Milliseconds()),
			}
			if err != nil {
				attrs = append(attrs, slog.String("error_type", fmt.Sprintf("%T", err)))
			}
			if spanContext := trace.SpanContextFromContext(c.Request().Context()); spanContext.IsValid() {
				attrs = append(attrs, slog.String("trace_id", spanContext.TraceID().String()),
					slog.String("span_id", spanContext.SpanID().String()))
			}
			slog.LogAttrs(c.Request().Context(), slog.LevelInfo, "http request", attrs...)
			return err
		}
	}
}
