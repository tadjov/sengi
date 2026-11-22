package main

import (
	"fmt"

	"github.com/tadjov/sengi/internal/embedding"
	"github.com/tadjov/sengi/internal/storage/factory"
)

type NewsSearchConfig struct {
	StorageConfig   factory.StorageConfig
	EmbeddingConfig embedding.Config
}

func loadConfig() (*NewsSearchConfig, error) {
	storageCfg, err := factory.LoadEnv()
	if err != nil {
		return nil, fmt.Errorf("load storage configuration: %w", err)
	}

	embed, err := embedding.LoadConfigFromEnv()
	if err != nil {
		return nil, fmt.Errorf("load embedding configuration: %w", err)
	}

	return &NewsSearchConfig{
		StorageConfig:   *storageCfg,
		EmbeddingConfig: *embed,
	}, nil
}
