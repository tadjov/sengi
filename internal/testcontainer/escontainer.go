package testcontainer

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/elasticsearch"
	"github.com/testcontainers/testcontainers-go/wait"
)

// ElasticsearchVersion is the version NewESContainer runs.
const ElasticsearchVersion = "9.5.5"

const (
	elasticsearchImage  = "docker.elastic.co/elasticsearch/elasticsearch:" + ElasticsearchVersion
	elasticsearchDigest = "sha256:13b4a40bb095774f5fda635e45fd5677cc6a26565e42b24997b0c72b88e75984"
)

// digestPin adds the digest at creation, since the module reads its defaults from the bare tag.
type digestPin struct {
	image  string
	digest string
}

func (p digestPin) Description() string {
	return "digest pin"
}

func (p digestPin) Substitute(image string) (string, error) {
	if image != p.image {
		return image, nil
	}
	return image + "@" + p.digest, nil
}

// ESContainer represents a running Elasticsearch test container
type ESContainer struct {
	Container testcontainers.Container
	Address   string
}

// NewESContainer starts an Elasticsearch test container
func NewESContainer(ctx context.Context, tb testing.TB) *ESContainer {
	tb.Helper()
	if testing.Short() {
		tb.Skip("skipping testcontainer-backed test in -short mode")
	}

	// Security is off because the plaintext probe below would never pass TLS.
	// ES_JAVA_OPTS overrides the module's 2 GB heap with 512 MB.
	esContainer, err := elasticsearch.Run(ctx,
		elasticsearchImage,
		testcontainers.WithImageSubstitutors(digestPin{image: elasticsearchImage, digest: elasticsearchDigest}),
		testcontainers.WithEnv(map[string]string{
			"xpack.security.enabled": "false",
			"discovery.type":         "single-node",
			"ES_JAVA_OPTS":           "-Xms512m -Xmx512m",
		}),
		testcontainers.WithWaitStrategy(
			wait.ForHTTP("/").
				WithPort("9200").
				WithStartupTimeout(180*time.Second),
		),
	)
	if err != nil {
		tb.Fatalf("failed to start elasticsearch container: %v", err)
	}

	tb.Cleanup(func() {
		if err := testcontainers.TerminateContainer(esContainer); err != nil {
			tb.Logf("failed to terminate elasticsearch container: %v", err)
		}
	})

	host, err := esContainer.Host(ctx)
	if err != nil {
		tb.Fatalf("failed to get elasticsearch host: %v", err)
	}

	port, err := esContainer.MappedPort(ctx, "9200")
	if err != nil {
		tb.Fatalf("failed to get elasticsearch port: %v", err)
	}

	address := fmt.Sprintf("http://%s:%s", host, port.Port())

	return &ESContainer{
		Container: esContainer,
		Address:   address,
	}
}
