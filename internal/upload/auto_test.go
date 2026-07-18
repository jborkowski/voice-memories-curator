package upload

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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

func TestAuthenticatedRepoURL(t *testing.T) {
	cfg := &config.Config{HFToken: "hf_test", HFRepo: "j14i/voice-memories"}
	url := authenticatedDatasetURL(cfg)
	if !strings.HasPrefix(url, "https://x-access-token:hf_test@huggingface.co/datasets/j14i/voice-memories") {
		t.Fatalf("bad url: %s", url)
	}
	if strings.Contains(url, "Bearer") {
		t.Fatal("must not use Bearer in git URL")
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
