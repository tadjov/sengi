package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tadjov/tusker/internal/bench/engine"
	"github.com/tadjov/tusker/internal/bench/report"
	pkgtesting "github.com/tadjov/tusker/pkg/testing"
	"github.com/testcontainers/testcontainers-go"
)

// latencyOnlyTrack is a one-engine track whose single query is block, written
// under engines: as name.
func latencyOnlyTrack(t *testing.T, name, engineYAML, block string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "trec"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "spec.yaml"), fmt.Appendf(nil, `schema_version: 1
id: settings
kind: fts
engines:
  %s:
%s
runs:
  warmup: 1
  iterations: 1
jobs:
  - name: settings
    suite: suite.yaml
    engines: [%s]
`, name, engineYAML, name), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "suite.yaml"), fmt.Appendf(nil, `schema_version: 1
id: settings
queries:
  - id: q1
    text: climate
    engines:
      %s: { query: '%s' }
`, name, block), 0o644))
	return dir
}

func onlyReport(t *testing.T, track string) *report.Report {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(track, "reports", "*-run-*.json"))
	require.NoError(t, err)
	require.Len(t, paths, 1)
	rpt, err := report.ReadJSON(paths[0])
	require.NoError(t, err)
	return rpt
}

func containerName(t *testing.T, c testcontainers.Container) string {
	t.Helper()
	name, err := c.Name(context.Background())
	require.NoError(t, err)
	return strings.TrimPrefix(name, "/")
}

func TestRun_ReportRecordsWhatThePostgresServerRanWith(t *testing.T) {
	ctx := context.Background()
	pg := pkgtesting.NewPGContainerWithConfig(ctx, t, pkgtesting.PGConfig{ServerFlags: []string{"-c", "work_mem=7MB"}})
	conn, err := pgx.Connect(ctx, pg.ConnString)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `INSERT INTO articles (id, title, content, url, language) VALUES ($1, 'climate', 'climate', 'https://example.com/a', 'eng')`, uuid.New())
	require.NoError(t, err)
	require.NoError(t, conn.Close(ctx))
	track := latencyOnlyTrack(t, "pg", fmt.Sprintf(`    type: postgres
    connection: %q
    container: %s
    connection_settings:
      plan_cache_mode: force_custom_plan`, pg.ConnString, containerName(t, pg.Container)), "SELECT id FROM articles LIMIT 10")

	out, err := execute(t, newRunCmd(), track, "--latency-only")
	require.NoError(t, err, out)

	info := onlyReport(t, track).Environment.Engines["pg"]
	require.NotNil(t, info.Index)
	server := info.Index.PostgresServer
	require.NotNil(t, server, "index error: %s", info.Index.Error)
	assert.True(t, strings.HasPrefix(server.Version, "PostgreSQL 18"), server.Version)
	assert.Contains(t, server.Settings, engine.PostgresSetting{Name: "work_mem", Setting: "7168", Unit: "kB", Source: "command line"})
	extensions := map[string]string{}
	for _, ext := range server.Extensions {
		extensions[ext.Name] = ext.Version
	}
	assert.NotEmpty(t, extensions["vector"], "pgvector with its version; got %v", server.Extensions)
	assert.Equal(t, "force_custom_plan", info.Index.Postgres.Settings["plan_cache_mode"], "the spec's connection setting, read back from the session")
	assert.True(t, server.Limits.CPUMax != "" || server.Limits.Unreadable != "", "cgroup limits or why they are unread: %+v", server.Limits)
	require.NotNil(t, info.Container)
	assert.Equal(t, "pgvector/pgvector:pg18", info.Container.Image)
	assert.True(t, strings.HasPrefix(info.Container.ImageID, "sha256:"), info.Container.ImageID)
	assert.Empty(t, info.Container.Note)
}

func TestRun_AServerThatCannotReadItsCgroupLimitsRecordsWhyAndRuns(t *testing.T) {
	ctx := context.Background()
	pg := pkgtesting.NewPGContainerWithCleanup(ctx, t)
	conn, err := pgx.Connect(ctx, pg.ConnString)
	require.NoError(t, err)
	for _, stmt := range []string{
		`INSERT INTO articles (id, title, content, url, language) VALUES (gen_random_uuid(), 'climate', 'climate', 'https://example.com/a', 'eng')`,
		`CREATE ROLE bench_reader LOGIN PASSWORD 'reader'`,
		`GRANT SELECT ON ALL TABLES IN SCHEMA public TO bench_reader`,
	} {
		_, err = conn.Exec(ctx, stmt)
		require.NoError(t, err, stmt)
	}
	require.NoError(t, conn.Close(ctx))
	reader, err := url.Parse(pg.ConnString)
	require.NoError(t, err)
	reader.User = url.UserPassword("bench_reader", "reader")
	track := latencyOnlyTrack(t, "pg", fmt.Sprintf(`    type: postgres
    connection: %q`, reader.String()), "SELECT id FROM articles LIMIT 10")

	out, err := execute(t, newRunCmd(), track, "--latency-only")
	require.NoError(t, err, out)

	rpt := onlyReport(t, track)
	info := rpt.Environment.Engines["pg"]
	require.NotNil(t, info.Index)
	server := info.Index.PostgresServer
	require.NotNil(t, server, "index error: %s", info.Index.Error)
	assert.NotEmpty(t, server.Limits.Unreadable, "a role that may not read server files says why")
	assert.Empty(t, server.Limits.CPUMax)
	assert.NotEmpty(t, server.Settings, "the rest of the server is still recorded")
	assert.Nil(t, aggregatedEngine(t, rpt.Jobs[0], "pg").ServerLatency, "postgres reports no server time")
}

func TestRun_ReportRecordsWhatTheElasticsearchClusterRanWith(t *testing.T) {
	ctx := context.Background()
	es := pkgtesting.NewESContainer(ctx, t)
	docID := uuid.New().String()
	esRequest(t, http.MethodPut, es.Address+"/news", `{"settings": {"number_of_replicas": 0}}`)
	esRequest(t, http.MethodPut, es.Address+"/news/_doc/"+docID+"?refresh=true", `{"id": "`+docID+`", "title": "climate"}`)
	track := latencyOnlyTrack(t, "es", fmt.Sprintf(`    type: elasticsearch
    connection: %q
    index: news`, es.Address), `{"query": {"match": {"title": "climate"}}}`)

	out, err := execute(t, newRunCmd(), track, "--latency-only")
	require.NoError(t, err, out)

	info := onlyReport(t, track).Environment.Engines["es"]
	require.NotNil(t, info.Index)
	cluster := info.Index.ElasticsearchCluster
	require.NotNil(t, cluster, "index error: %s", info.Index.Error)
	assert.Equal(t, "8.12.0", info.Version)
	assert.NotEmpty(t, cluster.BuildHash)
	assert.Equal(t, "0", cluster.IndexSettings["index.number_of_replicas"])
	require.NotEmpty(t, cluster.Nodes)
	assert.Equal(t, int64(512<<20), cluster.Nodes[0].HeapMaxBytes, "the container's -Xmx512m")
	assert.Nil(t, info.Container, "the spec names no container")
}

func esRequest(t *testing.T, method, url, body string) {
	t.Helper()
	req, err := http.NewRequest(method, url, bytes.NewBufferString(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Less(t, resp.StatusCode, 300, "%s %s", method, url)
}

func TestRun_AContainerDockerCannotInspectIsRecordedAsANote(t *testing.T) {
	tr := judgedTrack(t, nil)
	missing := "tusker-bench-test-" + uuid.NewString()
	editFile(t, filepath.Join(tr.dir, "spec.yaml"), "    index: a\n", "    index: a\n    container: "+missing+"\n")

	_, err := execute(t, newRunCmd(), tr.dir)

	require.NoError(t, err, "a failing docker inspect never fails the run")
	engines := tr.latestReport(t).Environment.Engines
	require.NotNil(t, engines["a"].Container)
	assert.Equal(t, missing, engines["a"].Container.Name)
	assert.NotEmpty(t, engines["a"].Container.Note)
	assert.Empty(t, engines["a"].Container.Image)
	assert.Nil(t, engines["b"].Container, "b names no container")
}

func TestRun_NoContainerNamedRecordsNothingAndWarnsNothing(t *testing.T) {
	tr := judgedTrack(t, nil)

	out, err := execute(t, newRunCmd(), tr.dir)

	require.NoError(t, err)
	for name, info := range tr.latestReport(t).Environment.Engines {
		assert.Nil(t, info.Container, name)
	}
	assert.NotContains(t, out, "container of")
}
