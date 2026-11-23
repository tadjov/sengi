package testcontainer

import (
	"context"
	"fmt"
	"net/http"
	"sync"
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

type ESContainer struct {
	Container testcontainers.Container
	Address   string
}

var (
	sharedESMu sync.Mutex
	sharedES   *ESContainer
	esInUse    runLock
)

// NewESContainer hands the test the Elasticsearch server shared by every test
// of the go test run. Indices are cluster-wide, so the test holds the server
// alone until it ends, across packages too, and its indices are deleted then.
// The server is never terminated here: the testcontainers reaper removes it
// when the run ends.
func NewESContainer(ctx context.Context, tb testing.TB) *ESContainer {
	tb.Helper()
	if testing.Short() {
		tb.Skip("skipping testcontainer-backed test in -short mode")
	}
	name := runScopedName("es", "")
	if err := esInUse.acquire(name); err != nil {
		tb.Fatalf("failed to take the shared elasticsearch server: %v", err)
	}
	tb.Cleanup(esInUse.release)

	sharedESMu.Lock()
	defer sharedESMu.Unlock()
	if sharedES == nil {
		container, err := startES(context.WithoutCancel(ctx), name)
		if err != nil {
			tb.Fatalf("failed to start elasticsearch container: %v", err)
		}
		sharedES = container
	}
	server := sharedES
	tb.Cleanup(func() {
		if err := server.deleteIndices(context.WithoutCancel(ctx)); err != nil {
			tb.Logf("failed to delete the test's elasticsearch indices: %v", err)
		}
	})
	return server
}

func startES(ctx context.Context, name string) (*ESContainer, error) {
	// Security is off because the plaintext probe below would never pass TLS.
	// ES_JAVA_OPTS overrides the module's 2 GB heap with 512 MB.
	esContainer, err := elasticsearch.Run(ctx,
		elasticsearchImage,
		testcontainers.WithImageSubstitutors(digestPin{image: elasticsearchImage, digest: elasticsearchDigest}),
		testcontainers.WithReuseByName(name),
		testcontainers.WithEnv(map[string]string{
			"xpack.security.enabled":           "false",
			"discovery.type":                   "single-node",
			"ES_JAVA_OPTS":                     "-Xms512m -Xmx512m",
			"action.destructive_requires_name": "false",
		}),
		testcontainers.WithWaitStrategy(
			wait.ForHTTP("/").
				WithPort("9200").
				WithStartupTimeout(180*time.Second),
		),
	)
	if err != nil {
		return nil, err
	}
	host, err := esContainer.Host(ctx)
	if err != nil {
		return nil, fmt.Errorf("get elasticsearch host: %w", err)
	}
	port, err := esContainer.MappedPort(ctx, "9200")
	if err != nil {
		return nil, fmt.Errorf("get elasticsearch port: %w", err)
	}
	return &ESContainer{Container: esContainer, Address: fmt.Sprintf("http://%s:%s", host, port.Port())}, nil
}

func (c *ESContainer) deleteIndices(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.Address+"/*", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("delete indices: status %d", resp.StatusCode)
	}
	return nil
}
