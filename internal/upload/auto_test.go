package upload

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/jborkowski/vmc/internal/config"
)

func TestFilterMissingRemote(t *testing.T) {
	local := []string{
		"/tmp/shard_0001.parquet",
		"/tmp/shard_0002.parquet",
		"/tmp/shard_0003.parquet",
	}
	remote := map[string]struct{}{
		"shard_0001.parquet": {},
		"shard_0003.parquet": {},
	}
	got := filterMissingRemote(local, remote)
	if len(got) != 1 || filepath.Base(got[0]) != "shard_0002.parquet" {
		t.Fatalf("got %v", got)
	}
}

func TestFilterMissingRemoteAllPresent(t *testing.T) {
	local := []string{"/tmp/shard_0001.parquet"}
	remote := map[string]struct{}{"shard_0001.parquet": {}}
	got := filterMissingRemote(local, remote)
	if len(got) != 0 {
		t.Fatalf("expected empty, got %v", got)
	}
}

func TestBatchPaths(t *testing.T) {
	paths := []string{"a", "b", "c", "d", "e"}
	got := batchPaths(paths, 2)
	if len(got) != 3 || len(got[0]) != 2 || len(got[1]) != 2 || len(got[2]) != 1 {
		t.Fatalf("got %#v", got)
	}
	got = batchPaths(paths, 0) // default size
	if len(got) != 3 {
		t.Fatalf("default batch size: %#v", got)
	}
	if batchPaths(nil, 2) != nil {
		t.Fatal("empty should be nil")
	}
}

func TestForceDoesNotSkipRemoteFilter(t *testing.T) {
	// Simulate RunWithOptions remote filter: force must not re-select present shards.
	local := []string{"/data/shard_0001.parquet", "/data/shard_0002.parquet"}
	remote := map[string]struct{}{
		"shard_0001.parquet": {},
		"shard_0002.parquet": {},
	}
	missing := filterMissingRemote(local, remote)
	if len(missing) != 0 {
		t.Fatalf("--force must not re-upload present partitions, got %v", missing)
	}
}

func TestListRemoteShardNamesTreePath(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/datasets/j14i/voice-memories/tree/main/data", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"type": "file", "path": "data/shard_0001.parquet"},
			{"type": "file", "path": "data/shard_0002.parquet"},
			{"type": "directory", "path": "data/nested"},
		})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	cfg := &config.Config{
		HFToken:   "tok",
		HFRepo:    "j14i/voice-memories",
		HFBaseURL: ts.URL,
	}
	names, err := listRemoteShardNames(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 {
		t.Fatalf("want 2, got %d %v", len(names), names)
	}
	if _, ok := names["shard_0001.parquet"]; !ok {
		t.Fatal("missing shard_0001")
	}
}

func TestRewriteEnvToken(t *testing.T) {
	env := []string{"PATH=/bin", "HF_TOKEN=stale", "HOME=/tmp"}
	got := rewriteEnvToken(env, "fresh")
	found := false
	for _, e := range got {
		if e == "HF_TOKEN=fresh" {
			found = true
		}
		if e == "HF_TOKEN=stale" {
			t.Fatal("stale token left in env")
		}
	}
	if !found {
		t.Fatalf("missing fresh token: %v", got)
	}
}

func TestIsRepoExistsErr(t *testing.T) {
	if !isRepoExistsErr(errString("status 409: already created")) {
		t.Fatal("409 should count as exists")
	}
	if isRepoExistsErr(errString("status 401")) {
		t.Fatal("401 is not exists")
	}
}

type errString string

func (e errString) Error() string { return string(e) }
