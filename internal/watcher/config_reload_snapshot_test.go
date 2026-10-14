package watcher

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestConfigReloadDoesNotAcknowledgeConcurrentSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	write := func(port string) {
		t.Helper()
		if err := os.WriteFile(path, []byte("port: "+port+"\nauth-dir: "+dir+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("8317")
	var observed []int
	w := &Watcher{configPath: path, authDir: dir}
	w.reloadCallback = func(cfg *config.Config) {
		observed = append(observed, cfg.Port)
		if len(observed) == 1 {
			// Simulate a successful management save after the old file was
			// parsed, but before the reload records its applied hash.
			write("8318")
		}
	}
	w.ReloadConfigIfChanged()
	w.ReloadConfigIfChanged()
	w.ReloadConfigIfChanged()
	if !reflect.DeepEqual(observed, []int{8317, 8318}) {
		t.Fatalf("concurrent save was lost or unchanged file reloaded: %v", observed)
	}
}
