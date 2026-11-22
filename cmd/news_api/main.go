// Package main Sengi API
// @title Sengi API
// @version 1.0
// @description A full-text and semantic search engine for exploring multilingual news headlines and articles
// @termsOfService http://swagger.io/terms/
// @contact.name API Support
// @contact.email support@sengi.sh
// @license.name Apache 2.0
// @license.url https://opensource.org/licenses/Apache-2.0
// @BasePath /
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/labstack/echo/v4"
	"github.com/tadjov/sengi/internal/api/router"
	"github.com/tadjov/sengi/internal/api/server"
	"github.com/tadjov/sengi/internal/embedding"
	"github.com/tadjov/sengi/internal/env"
)

func main() {
	slog.SetLogLoggerLevel(slog.LevelDebug)

	if err := env.LoadDotEnv("cmd/news_api/.env"); err != nil {
		slog.Warn("Continuing with the environment as it is", "error", err)
	}

	serverCfg, err := server.LoadConfig()
	if err != nil {
		slog.Error("Failed to load server configuration", "error", err)
		os.Exit(1)
	}

	cfg, err := loadConfig()
	if err != nil {
		slog.Error("Failed to load app configuration", "error", err)
		os.Exit(1)
	}

	backend, err := openSearchBackend(context.Background(), cfg)
	if err != nil {
		slog.Error("Failed to open search backend", "error", err)
		os.Exit(1)
	}

	s, err := server.New(serverCfg, backend.health)
	if err != nil {
		backend.close()
		slog.Error("Failed to create server", "error", err)
		os.Exit(1)
	}
	s.SetupMiddlewares().
		SetupValidator().
		SetupErrorHandler().
		SetupHealthChecks("/health").
		SetupOpenApi("/swagger/*")

	s.Echo.GET("/", func(c echo.Context) error {
		return c.String(200, "Sengi API is running")
	})

	slog.Info("Search paradigms beyond full text", "semantic", backend.semantic != nil, "hybrid", backend.hybrid != nil)
	router.NewSearchRouter(s.Echo, router.Searchers{
		FullText: backend.fts,
		Semantic: backend.semantic,
		Hybrid:   backend.hybrid,
	}).Bind()

	err = s.Start()
	backend.close()
	slog.Info("Search backend closed")
	if err != nil {
		slog.Error("Server stopped with an error", "error", err)
		os.Exit(1)
	}
}

// newQueryEmbedder must use the model and width the corpus was loaded with.
func newQueryEmbedder(client embedding.Client, cfg embedding.Config) *embedding.Embedder {
	var opts []embedding.EmbedderOption
	if cfg.Model != "" {
		opts = append(opts, embedding.WithModel(cfg.Model))
	}
	if cfg.Dim != 0 {
		opts = append(opts, embedding.WithDim(cfg.Dim))
	}
	return embedding.NewEmbedder(client, opts...)
}
