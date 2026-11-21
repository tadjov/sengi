package testing

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/elasticsearch"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	elasticsearchImage  = "docker.elastic.co/elasticsearch/elasticsearch:8.12.0"
	elasticsearchDigest = "sha256:ec72548cf833e58578d8ff40df44346a49480b2c88a4e73a91e1b85ec7ef0d44"
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

	// Elasticsearch 8 enables security, and with it TLS, by default; the
	// readiness probe below speaks plaintext and would poll until it times out.
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
