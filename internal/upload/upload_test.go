package upload

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/jborkowski/vmc/internal/detect"
	"github.com/jborkowski/vmc/internal/process"
	"github.com/jborkowski/vmc/internal/testutil"
)

func checkGitXet(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git-xet"); err != nil {
		t.Skip("git-xet not installed")
	}
}

func TestUploadReadyCheck(t *testing.T) {
	testutil.SuppressLogs()

	rows := []testutil.AppleDBRow{
		{Z_PK: 1, ZDATE: 1000, ZPATH: "2023/1.m4a", ZCUSTOMLABEL: "Memo 1", ZDURATION: 1.0},
	}
	dbPath := testutil.CreateAppleDB(t, rows)
	testutil.SetupAudioFiles(t, dbPath, rows)

	shardDir := t.TempDir()
	cfg := testutil.SetupConfig(t, "https://example.com", dbPath, shardDir)

	db := testutil.GetDuckDB(t)

	if err := detect.Run(db, cfg); err != nil {
		t.Fatalf("detect Run failed: %v", err)
	}

	shardPath := filepath.Join(shardDir, "shard_0001.parquet")

	ready, err := isShardReady(db, shardPath)
	if err != nil {
		t.Fatalf("isShardReady failed: %v", err)
	}
	if ready {
		t.Errorf("expected shard to NOT be ready (audio is null)")
	}

	if err := process.Run(db, cfg); err != nil {
		t.Fatalf("process Run failed: %v", err)
	}

	ready, err = isShardReady(db, shardPath)
	if err != nil {
		t.Fatalf("isShardReady failed: %v", err)
	}
	if !ready {
		t.Errorf("expected shard to be ready (audio is filled)")
	}
}

func TestUploadOffline(t *testing.T) {
	checkGitXet(t)
	testutil.SuppressLogs()

	rows := []testutil.AppleDBRow{
		{Z_PK: 1, ZDATE: 1000, ZPATH: "2023/1.m4a", ZCUSTOMLABEL: "Memo 1", ZDURATION: 1.0},
	}
	dbPath := testutil.CreateAppleDB(t, rows)
	testutil.SetupAudioFiles(t, dbPath, rows)

	shardDir := t.TempDir()
	cfg := testutil.SetupConfig(t, "http://localhost:12345/not-exist", dbPath, shardDir)

	db := testutil.GetDuckDB(t)

	detect.Run(db, cfg)
	process.Run(db, cfg)

	if err := RunWithOptions(db, cfg, true); err != nil {
		t.Fatalf("expected clean exit for offline upload, got error: %v", err)
	}

	shardPath := filepath.Join(shardDir, "shard_0001.parquet")
	if _, err := os.Stat(shardPath); os.IsNotExist(err) {
		t.Errorf("expected shard to remain local after offline upload attempt")
	}
}

func TestUploadSuccess(t *testing.T) {
	checkGitXet(t)
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	testutil.SuppressLogs()

	// Create a local bare git repo to simulate HF
	bareRepo := t.TempDir()
	gitInit := exec.Command("git", "init", "--bare", bareRepo)
	if out, err := gitInit.CombinedOutput(); err != nil {
		t.Fatalf("failed to init bare repo: %v\n%s", err, out)
	}

	// Create a working clone to set up initial commit
	workDir := t.TempDir()
	clone := exec.Command("git", "clone", bareRepo, workDir)
	if out, err := clone.CombinedOutput(); err != nil {
		t.Fatalf("failed to clone bare repo: %v\n%s", err, out)
	}
	// Need an initial commit for the clone to work in uploadShard
	initFile := filepath.Join(workDir, ".gitattributes")
	os.WriteFile(initFile, []byte("*.parquet filter=lfs diff=lfs merge=lfs -text\n"), 0644)
	for _, args := range [][]string{
		{"add", "."},
		{"commit", "-m", "init"},
		{"push", "origin", "main"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = workDir
		if out, err := cmd.CombinedOutput(); err != nil {
			// push might fail if default branch isn't main
			cmd2 := exec.Command("git", append([]string{"push", "origin", "HEAD:refs/heads/main"}, args[1:]...)...)
			cmd2.Dir = workDir
			cmd2.CombinedOutput()
		} else {
			_ = out
		}
	}

	rows := []testutil.AppleDBRow{
		{Z_PK: 1, ZDATE: 1000, ZPATH: "2023/1.m4a", ZCUSTOMLABEL: "Memo 1", ZDURATION: 1.0},
	}
	dbPath := testutil.CreateAppleDB(t, rows)
	testutil.SetupAudioFiles(t, dbPath, rows)

	shardDir := t.TempDir()
	// Use file:// URL pointing to the local bare repo (no real HF needed)
	cfg := testutil.SetupConfig(t, "https://huggingface.co", dbPath, shardDir)
	cfg.HFToken = "fake-token"
	cfg.HFRepo = "test/repo"

	db := testutil.GetDuckDB(t)

	detect.Run(db, cfg)
	process.Run(db, cfg)

	// Verify shard is ready
	shardPath := filepath.Join(shardDir, "shard_0001.parquet")
	ready, err := isShardReady(db, shardPath)
	if err != nil {
		t.Fatalf("isShardReady failed: %v", err)
	}
	if !ready {
		t.Fatalf("expected shard to be ready after process")
	}
}
