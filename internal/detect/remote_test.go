package detect

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/jborkowski/vmc/internal/config"
	"github.com/jborkowski/vmc/internal/testutil"
)

func TestFetchRemoteRecordingIDsTreePath(t *testing.T) {
	testutil.SuppressLogs()

	// Minimal parquet with recording_id via DuckDB.
	db := testutil.GetDuckDB(t)
	tmpDir := t.TempDir()
	parquetPath := filepath.Join(tmpDir, "shard_0001.parquet")
	if _, err := db.Exec(`
		COPY (SELECT CAST(42 AS BIGINT) AS recording_id) TO '` + parquetPath + `' (FORMAT PARQUET)
	`); err != nil {
		t.Fatalf("write parquet: %v", err)
	}
	parquetBytes, err := os.ReadFile(parquetPath)
	if err != nil {
		t.Fatalf("read parquet: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/datasets/test/repo/tree/main/data", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"type":"file","path":"data/shard_0001.parquet","size":123},
			{"type":"directory","path":"data/nested"}
		]`))
	})
	mux.HandleFunc("/datasets/test/repo/resolve/main/data/shard_0001.parquet", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(parquetBytes)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	cfg := &config.Config{
		HFToken:   "tok",
		HFRepo:    "test/repo",
		HFBaseURL: ts.URL,
	}
	ids, err := fetchRemoteRecordingIDs(cfg)
	if err != nil {
		t.Fatalf("fetchRemoteRecordingIDs: %v", err)
	}
	if len(ids) != 1 || ids[0] != 42 {
		t.Fatalf("expected [42], got %v", ids)
	}
}
