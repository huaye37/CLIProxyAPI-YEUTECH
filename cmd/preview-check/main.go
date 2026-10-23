// Command preview-check validates live configuration without startup rewrites,
// credential refresh, model probes, watchers, transport or inference.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"gopkg.in/yaml.v3"
)

func main() {
	if len(os.Args) != 2 {
		os.Exit(2)
	}
	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		os.Exit(1)
	}
	var cfg config.Config
	if yaml.Unmarshal(data, &cfg) != nil || cfg.Port <= 0 || cfg.AuthDir == "" {
		os.Exit(1)
	}
	files, err := os.ReadDir(cfg.AuthDir)
	if err != nil {
		os.Exit(1)
	}
	providers := map[string]int{}
	for _, file := range files {
		if file.IsDir() || filepath.Ext(file.Name()) != ".json" {
			continue
		}
		raw, readErr := os.ReadFile(filepath.Join(cfg.AuthDir, file.Name()))
		if readErr != nil {
			os.Exit(1)
		}
		var account map[string]any
		if json.Unmarshal(raw, &account) != nil {
			os.Exit(1)
		}
		provider, _ := account["type"].(string)
		switch provider {
		case "codex", "antigravity", "gemini", "claude", "xai", "iflow", "qwen":
		default:
			provider = "other"
		}
		providers[provider]++
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"ok": true, "configuredPort": cfg.Port, "credentialCounts": providers, "inferenceEnabled": false, "credentialRefreshEnabled": false})
}
