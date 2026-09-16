package adapters

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Live wiring vs build-time verification are two different claims:
//   - Adapter.Verified (docs/adapters/verification.md): the hook surface was
//     checked against live harness docs at build time — "this harness CAN
//     learn once wired".
//   - Wiring below: what is actually installed on THIS machine right now —
//     "this harness DOES capture/inject today".
// Doctor prints both, so "verified but not wired" is visible instead of
// reading as if the harness were already learning (the 2026-09-16 review
// finding: doctor reported 3 harnesses learning while only Claude Code was
// actually wired on the machine).
type WiringState int

const (
	WiringAbsent  WiringState = iota // harness home not detected on this machine
	WiringNotWired                    // harness present, no palate wiring found
	WiringWired                       // palate wiring found (hooks or MCP)
)

// Wiring is one harness's live wiring snapshot.
type Wiring struct {
	State  WiringState
	Detail string
}

// backupBeforeWrite copies an existing config to
// "<path>.bak-palate-<timestamp>" before the first palate mutation — the
// skill-concierge installer convention (back up before every config
// mutation, never lose the previous state). No-op when the file does not
// exist yet. The original file's permission bits are preserved.
func backupBeforeWrite(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	mode := os.FileMode(0600)
	if info, statErr := os.Stat(path); statErr == nil {
		mode = info.Mode().Perm()
	}
	bak := path + ".bak-palate-" + time.Now().Format("20060102-150405")
	return os.WriteFile(bak, data, mode)
}

func homePath(parts ...string) (string, bool) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", false
	}
	return filepath.Join(append([]string{home}, parts...)...), true
}

func dirPresent(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// pluginHookGlob matches the plugin-vehicle hook manifest under dir's plugin
// cache — the cache/<marketplace>/<plugin>/<version>/hooks/hooks.json layout
// both Claude Code and Codex plugin installs use.
func pluginHookGlob(dir string) (string, bool) {
	matches, err := filepath.Glob(filepath.Join(dir, "plugins", "cache", "*", "palate", "*", "hooks", "hooks.json"))
	if err != nil || len(matches) == 0 {
		return "", false
	}
	return matches[0], true
}

func pluginVersionOf(hooksJSON string) string {
	// .../<version>/hooks/hooks.json -> <version>
	return filepath.Base(filepath.Dir(filepath.Dir(hooksJSON)))
}

func wiringClaudeCode() Wiring {
	dir, ok := homePath(".claude")
	if !ok || !dirPresent(dir) {
		return Wiring{State: WiringAbsent}
	}
	if hooksJSON, ok := pluginHookGlob(dir); ok {
		return Wiring{State: WiringWired, Detail: "plugin hooks v" + pluginVersionOf(hooksJSON)}
	}
	// Enabled plugin with a marketplace checkout: hooks.json is read from the
	// marketplace copy (settings.json enabledPlugins decides activation).
	if data, err := os.ReadFile(filepath.Join(dir, "settings.json")); err == nil {
		var s map[string]interface{}
		if json.Unmarshal(data, &s) == nil {
			if ep, _ := s["enabledPlugins"].(map[string]interface{}); ep["palate@palate"] == true {
				return Wiring{State: WiringWired, Detail: "plugin enabled (settings.json enabledPlugins)"}
			}
		}
	}
	if n := CountPalateHooksTopLevel(filepath.Join(dir, "settings.json")); n > 0 {
		return Wiring{State: WiringWired, Detail: fmt.Sprintf("%d global settings hooks", n)}
	}
	return Wiring{State: WiringNotWired}
}

func wiringCommandCode() Wiring {
	dir, ok := homePath(".commandcode")
	if !ok || !dirPresent(dir) {
		return Wiring{State: WiringAbsent}
	}
	if n := CountPalateHooksTopLevel(filepath.Join(dir, "settings.json")); n > 0 {
		return Wiring{State: WiringWired, Detail: fmt.Sprintf("%d global settings hooks", n)}
	}
	if n := CountPalateHooksTopLevel(filepath.Join(dir, "settings.local.json")); n > 0 {
		return Wiring{State: WiringWired, Detail: fmt.Sprintf("%d global settings.local hooks", n)}
	}
	return Wiring{State: WiringNotWired}
}

func wiringZCode() Wiring {
	dir, ok := homePath(".zcode")
	if !ok || !dirPresent(dir) {
		return Wiring{State: WiringAbsent}
	}
	p, _ := homePath(".zcode", "cli", "config.json")
	if n := CountPalateHooksZCode(p); n > 0 {
		return Wiring{State: WiringWired, Detail: fmt.Sprintf("%d global config hooks", n)}
	}
	return Wiring{State: WiringNotWired}
}

func wiringCodex() Wiring {
	dir, ok := homePath(".codex")
	if !ok || !dirPresent(dir) {
		return Wiring{State: WiringAbsent}
	}
	// The Codex-native vehicle first: an mcp_servers.palate table in
	// ~/.codex/config.toml. Line scan for the table header — stdlib only,
	// no TOML dependency. A stale plugin-cache hooks.json must not outrank
	// this: the cache can survive an uninstalled plugin and would otherwise
	// misattribute the wiring (found live on this machine, 2026-09-16).
	if data, err := os.ReadFile(filepath.Join(dir, "config.toml")); err == nil {
		if strings.Contains(string(data), "[mcp_servers.palate]") {
			return Wiring{State: WiringWired, Detail: "MCP stdio (config.toml mcp_servers.palate)"}
		}
	}
	if hooksJSON, ok := pluginHookGlob(dir); ok {
		return Wiring{State: WiringWired, Detail: "plugin hooks v" + pluginVersionOf(hooksJSON)}
	}
	return Wiring{State: WiringNotWired}
}

func wiringCline() Wiring {
	dir, ok := homePath(".cline")
	if !ok || !dirPresent(dir) {
		return Wiring{State: WiringAbsent}
	}
	hooks, _ := homePath(".cline", "hooks")
	if n := CountClineHooks(hooks); n > 0 {
		return Wiring{State: WiringWired, Detail: fmt.Sprintf("%d hook shims", n)}
	}
	return Wiring{State: WiringNotWired}
}

// LiveWiring returns the live wiring snapshot keyed by Registry names.
// Harnesses without a dedicated detector report NotWired: they are MCP
// adapters, and nothing can be wired unless `palate serve` is configured in
// that harness — which there is no generic way to detect.
func LiveWiring() map[string]Wiring {
	out := map[string]Wiring{
		"Claude Code":  wiringClaudeCode(),
		"Command Code": wiringCommandCode(),
		"ZCode":        wiringZCode(),
		"Codex":        wiringCodex(),
		"Cline":        wiringCline(),
	}
	for _, a := range Registry {
		if _, ok := out[a.Name]; !ok {
			out[a.Name] = Wiring{State: WiringNotWired, Detail: "recall-only — configure `palate serve` (MCP) in this harness"}
		}
	}
	return out
}
