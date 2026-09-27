package daemon

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/paths"
)

// repo_instructions shares worktree_roots' silent failure mode: the key is
// matched against a registered checkout path, so a stale one - left by a move or
// an eject, or spelled a way this filesystem does not consider equal - steers
// nothing at all, and guidance that never arrives has no other symptom.
func TestReportUnusableRepoInstructions_NamesEntriesThatReachNoRun(t *testing.T) {
	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	registered := filepath.Join(t.TempDir(), "checkout")
	stale := filepath.Join(t.TempDir(), "moved-away")
	if _, err := d.InsertRepoWithID("repo1", registered, "https://example.com/owner/repo1", "main"); err != nil {
		t.Fatal(err)
	}

	configYAML := "repo_instructions:\n" +
		"  " + yamlPath(registered) + ":\n    document:\n      instructions: owned by docs\n" +
		"  " + yamlPath(stale) + ":\n    document:\n      instructions: owned by docs\n"
	if err := os.WriteFile(p.ConfigFile(), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	globalCfg, err := config.LoadGlobal(p.ConfigFile())
	if err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(oldLogger)

	reportUnusableRepoInstructions(d, globalCfg.RepoInstructions)

	got := logs.String()
	if !strings.Contains(got, "matches no registered repository") {
		t.Errorf("startup did not report the stale repo_instructions entry, logs:\n%s", got)
	}
	if strings.Contains(got, registered) {
		t.Errorf("a matching entry must not be reported, logs:\n%s", got)
	}
}
