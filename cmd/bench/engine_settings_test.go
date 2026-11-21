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
	"github.com/tadjov/sengi/internal/bench/report"
	pgstore "github.com/tadjov/sengi/internal/storage/pg"
	pkgtesting "github.com/tadjov/sengi/pkg/testing"
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
      maintenance_work_mem: 65536kB`, pg.ConnString, containerName(t, pg.Container)), "SELECT id FROM articles LIMIT 10")

	out, err := execute(t, newRunCmd(), track, "--latency-only")
	require.NoError(t, err, out)

	info := onlyReport(t, track).Environment.Engines["pg"]
	require.NotNil(t, info.Index)
	server := info.Index.Postgres
	require.NotNil(t, server, "index error: %s", info.Index.Error)
	assert.True(t, strings.HasPrefix(server.ServerVersion, "PostgreSQL 18"), server.ServerVersion)
	assert.Contains(t, server.ServerSettings, pgstore.ServerSetting{Name: "work_mem", Setting: "7168", Unit: "kB", Source: "command line"})
	extensions := map[string]string{}
	for _, ext := range server.Extensions {
		extensions[ext.Name] = ext.Version
	}
	assert.NotEmpty(t, extensions["vector"], "pgvector with its version; got %v", server.Extensions)
	assert.Equal(t, "64MB", server.Settings["maintenance_work_mem"], "the spec's 65536kB, read back from the session")
	for _, setting := range server.ServerSettings {
		assert.NotEqual(t, "default", setting.Source, setting.Name)
	}
	require.NotNil(t, server.Limits, "unreadable: %v", server.Unreadable)
	assert.NotEmpty(t, server.Limits.CPUMax)
	assert.Empty(t, server.Unreadable)
	require.NotNil(t, info.Index.Container)
	assert.Equal(t, "pgvector/pgvector:pg18", info.Index.Container.Image)
	assert.True(t, strings.HasPrefix(info.Index.Container.ImageID, "sha256:"), info.Index.Container.ImageID)
	assert.Empty(t, info.Index.Container.Note)
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
	server := info.Index.Postgres
	require.NotNil(t, server, "index error: %s", info.Index.Error)
	assert.Nil(t, server.Limits)
	require.Len(t, server.Unreadable, 1)
	assert.Contains(t, server.Unreadable[0], "cgroup limits", "a role that may not read server files says why")
	assert.NotEmpty(t, server.ServerSettings, "the rest of the server is still recorded")
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
	cluster := info.Index.Elasticsearch
	require.NotNil(t, cluster, "index error: %s", info.Index.Error)
	assert.Equal(t, "8.12.0", info.Version)
	assert.NotEmpty(t, cluster.BuildHash)
	assert.Equal(t, "0", cluster.Settings["index.number_of_replicas"])
	require.NotEmpty(t, cluster.Nodes)
	assert.Equal(t, int64(512<<20), cluster.Nodes[0].HeapMaxBytes, "the container's -Xmx512m")
	assert.Nil(t, info.Index.Container, "the spec names no container")
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
	missing := "sengi-bench-test-" + uuid.NewString()
	editFile(t, filepath.Join(tr.dir, "spec.yaml"), "    index: a\n", "    index: a\n    container: "+missing+"\n")

	_, err := execute(t, newRunCmd(), tr.dir)

	require.NoError(t, err, "a failing docker inspect never fails the run")
	engines := tr.latestReport(t).Environment.Engines
	require.NotNil(t, engines["a"].Index.Container)
	assert.Equal(t, missing, engines["a"].Index.Container.Name)
	assert.NotEmpty(t, engines["a"].Index.Container.Note)
	assert.Empty(t, engines["a"].Index.Container.Image)
	assert.Nil(t, engines["b"].Index.Container, "b names no container")
}

func TestRun_NoContainerNamedRecordsNothingAndWarnsNothing(t *testing.T) {
	tr := judgedTrack(t, nil)

	out, err := execute(t, newRunCmd(), tr.dir)

	require.NoError(t, err)
	for name, info := range tr.latestReport(t).Environment.Engines {
		assert.Nil(t, info.Index.Container, name)
	}
	assert.NotContains(t, out, "container of")
}
