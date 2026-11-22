package server

import (
	"log/slog"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

func requestLogger() echo.MiddlewareFunc {
	return middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
		LogStatus:   true,
		LogLatency:  true,
		LogURI:      true,
		LogError:    true,
		HandleError: true,
		LogValuesFunc: func(c echo.Context, v middleware.RequestLoggerValues) error {
			ctx := c.Request().Context()
			if v.Error == nil {
				slog.LogAttrs(ctx, slog.LevelInfo, "REQUEST",
					slog.String("uri", v.URI),
					slog.Int("status", v.Status),
					slog.Duration("latency", v.Latency),
				)
				return nil
			}
			slog.LogAttrs(ctx, slog.LevelError, "REQUEST_ERROR",
				slog.String("uri", v.URI),
				slog.Int("status", v.Status),
				slog.String("err", v.Error.Error()),
			)
			return nil
		},
	})
}
