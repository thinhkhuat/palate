package adapters

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestClineShimTableAndNames(t *testing.T) {
	want := map[string]string{
		"TaskStart":        "session-start",
		"TaskResume":       "session-start",
		"UserPromptSubmit": "user-prompt",
		"PostToolUse":      "post-tool-use",
		"TaskComplete":     "stop",
		"TaskCancel":       "stop",
	}
	if len(clineShims) != len(want) {
		t.Fatalf("clineShims has %d entries, want %d", len(clineShims), len(want))
	}
	for _, s := range clineShims {
		if want[s.Event] != s.PalateEvent {
			t.Fatalf("event %s maps to %q, want %q", s.Event, s.PalateEvent, want[s.Event])
		}
	}
}

func TestGenerateClineShimContract(t *testing.T) {
	shim := GenerateClineShim("TaskStart", "session-start", "/opt/palate/bin/palate")
	for _, want := range []string{
		"/opt/palate/bin/palate",
		"palate-cline-shim-v1",
		"'TaskStart'",
		"'session-start'",
		"workspaceRoots",
		"contextModification",
		"{ cancel: false }",
	} {
		if !strings.Contains(shim, want) {
			t.Fatalf("TaskStart shim missing %q", want)
		}
	}
	// The template placeholders must be fully consumed.
	if strings.Contains(shim, "PLACEHOLDER") {
		t.Fatal("generated shim still carries a placeholder")
	}
	// The shim must be ESM-safe for .mjs execution under node: import syntax,
	// no require().
	if strings.Contains(shim, "require(") {
		t.Fatal("mjs shim must not use require()")
	}
}

// A Windows install path carries backslashes (and `\001`-like segments that
// strict-mode ESM rejects as octal escapes); any path may carry a quote. The
// shim must still parse and hold the exact path.
func TestGenerateClineShimEscapesBinaryPath(t *testing.T) {
	bin := `C:\Users\run'ner\AppData\Local\Temp\001\palate.exe`
	shim := GenerateClineShim("TaskStart", "session-start", bin)
	_, rest, ok := strings.Cut(shim, "const PALATE_BIN = ")
	if !ok {
		t.Fatal("shim has no PALATE_BIN constant")
	}
	lit, _, _ := strings.Cut(rest, ";\n")
	var got string
	if err := json.Unmarshal([]byte(lit), &got); err != nil || got != bin {
		t.Fatalf("PALATE_BIN literal %s decodes to %q (%v), want %q", lit, got, err, bin)
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; the literal check above still ran")
	}
	f := filepath.Join(t.TempDir(), "TaskStart.mjs")
	if err := os.WriteFile(f, []byte(shim), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, "--check", f).CombinedOutput(); err != nil {
		t.Fatalf("shim with a Windows path does not parse: %v\n%s", err, out)
	}
}

// stubPalateScript writes a stub `palate` executable that records its stdin
// and replies with a Claude-hook-shaped JSON payload.
func stubPalateScript(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "stub-palate.sh")
	script := `#!/bin/sh
cat > "$STUB_STDIN_FILE"
printf '%s\n' '{"systemMessage":"NOTICE","hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"PREWARM"}}'
`
	if err := os.WriteFile(p, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	return p
}

func runShim(t *testing.T, shimPath, stdin string) (stdout string) {
	t.Helper()
	cmd := exec.Command("node", shimPath)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node %s failed: %v\n%s", shimPath, err, out)
	}
	return string(out)
}

func clineShimPalateEvent(event string) string {
	for _, s := range clineShims {
		if s.Event == event {
			return s.PalateEvent
		}
	}
	return ""
}

func TestClineShimEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Fatalf("node is required to run the shim e2e (Cline executes .mjs shims under node): %v", err)
	}
	dir := t.TempDir()
	stub := stubPalateScript(t, dir)
	stdinFile := filepath.Join(dir, "stdin-recorded.json")
	t.Setenv("STUB_STDIN_FILE", stdinFile)

	type shimCase struct {
		event       string
		payload     string
		wantPayload string // exact stdin the shim must forward to palate
		wantContext string // expected contextModification ("" = none)
	}
	cases := []shimCase{
		{
			event:       "TaskStart",
			payload:     `{"taskId":"no-such-task-g7e2e","workspaceRoots":["/tmp/ws"],"taskStart":{"taskMetadata":{}}}`,
			wantPayload: `{"session_id":"no-such-task-g7e2e","transcript_path":"","cwd":"/tmp/ws","source":"startup"}`,
			wantContext: "NOTICE\n\nPREWARM",
		},
		{
			event:       "TaskResume",
			payload:     `{"taskId":"t-resume","workspaceRoots":["/tmp/ws"],"taskResume":{"taskMetadata":{},"previousState":{}}}`,
			wantPayload: `{"session_id":"t-resume","transcript_path":"","cwd":"/tmp/ws","source":"resume"}`,
			wantContext: "NOTICE\n\nPREWARM",
		},
		{
			event:       "UserPromptSubmit",
			payload:     `{"taskId":"t1","workspaceRoots":["/tmp/ws"],"userPromptSubmit":{"prompt":"I prefer plain reporting","attachments":[]}}`,
			wantPayload: `{"prompt":"I prefer plain reporting"}`,
			wantContext: "NOTICE\n\nPREWARM",
		},
		{
			event:       "UserPromptSubmit-empty-prompt",
			payload:     `{"taskId":"t1","userPromptSubmit":{"prompt":"  "}}`,
			wantPayload: "",
			wantContext: "",
		},
		{
			event:       "PostToolUse",
			payload:     `{"taskId":"t1","postToolUse":{"toolName":"write_to_file","parameters":{"file_path":"/tmp/a.md"},"result":"ok","success":true,"executionTimeMs":1}}`,
			wantPayload: `{"tool_name":"write_to_file","tool_input":{"file_path":"/tmp/a.md"},"session_id":"t1"}`,
			wantContext: "NOTICE\n\nPREWARM",
		},
		{
			event:       "PostToolUse-quoted-param",
			payload:     `{"taskId":"t1","postToolUse":{"toolName":"replace_in_file","parameters":{"path":"\"/tmp/b.md\""},"result":"ok","success":true,"executionTimeMs":1}}`,
			wantPayload: `{"tool_name":"replace_in_file","tool_input":{"file_path":"/tmp/b.md"},"session_id":"t1"}`,
			wantContext: "NOTICE\n\nPREWARM",
		},
		{
			event:       "PostToolUse-no-file",
			payload:     `{"taskId":"t1","postToolUse":{"toolName":"execute_command","parameters":{"command":"ls"}}}`,
			wantPayload: "",
			wantContext: "",
		},
		{
			event:       "TaskComplete",
			payload:     `{"taskId":"t-done","workspaceRoots":["/tmp/ws"],"taskComplete":{"taskMetadata":{}}}`,
			wantPayload: `{"session_id":"t-done","transcript_path":"","cwd":"/tmp/ws"}`,
			wantContext: "NOTICE\n\nPREWARM",
		},
		{
			event:       "TaskCancel",
			payload:     `{"taskId":"t-cancel","workspaceRoots":["/tmp/ws"],"taskCancel":{"taskMetadata":{}}}`,
			wantPayload: `{"session_id":"t-cancel","transcript_path":"","cwd":"/tmp/ws"}`,
			wantContext: "NOTICE\n\nPREWARM",
		},
		{
			event:       "TaskStart-malformed-stdin",
			payload:     `not json`,
			wantPayload: "",
			wantContext: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.event, func(t *testing.T) {
			eventName := strings.SplitN(tc.event, "-", 2)[0]
			shim := filepath.Join(dir, tc.event+".mjs")
			if err := os.WriteFile(shim, []byte(GenerateClineShim(eventName, clineShimPalateEvent(eventName), stub)), 0755); err != nil {
				t.Fatal(err)
			}
			_ = os.Remove(stdinFile)
			stdout := runShim(t, shim, tc.payload)

			var got map[string]interface{}
			if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &got); err != nil {
				t.Fatalf("shim stdout is not one JSON line: %q", stdout)
			}
			if cancel, _ := got["cancel"].(bool); cancel {
				t.Fatalf("fail-open shim must never cancel: %v", got)
			}

			if tc.wantPayload == "" {
				if ctx, _ := got["contextModification"].(string); ctx != "" {
					t.Fatalf("expected no contextModification, got %q", ctx)
				}
				return
			}
			assertForwardedPayload(t, stdinFile, tc.wantPayload)
			if ctx, _ := got["contextModification"].(string); ctx != tc.wantContext {
				t.Fatalf("contextModification = %q, want %q", ctx, tc.wantContext)
			}
		})
	}
}

// assertForwardedPayload reads the stub's stdin record and compares it to the
// expected JSON object (key set and values, order-independent).
func assertForwardedPayload(t *testing.T, stdinFile, wantJSON string) {
	t.Helper()
	raw, err := os.ReadFile(stdinFile)
	if err != nil {
		t.Fatalf("stub palate was not called: %v", err)
	}
	var forwarded, want map[string]interface{}
	if json.Unmarshal(raw, &forwarded) != nil {
		t.Fatalf("forwarded payload not JSON: %q", raw)
	}
	if json.Unmarshal([]byte(wantJSON), &want) != nil {
		t.Fatal("test bug: wantJSON not JSON")
	}
	if len(forwarded) != len(want) {
		t.Fatalf("forwarded payload keys = %v, want %v", forwarded, want)
	}
	for k, wv := range want {
		fv, ok := forwarded[k]
		if !ok {
			t.Fatalf("forwarded payload missing key %q (full: %v)", k, forwarded)
		}
		// Values may be nested objects (tool_input) — compare canonically.
		fj, _ := json.Marshal(fv)
		wj, _ := json.Marshal(wv)
		if string(fj) != string(wj) {
			t.Fatalf("forwarded payload[%q] = %s, want %s (full: %v)", k, fj, wj, forwarded)
		}
	}
}

func TestClineShimResolvesTranscriptFromMetadata(t *testing.T) {
	// transcriptFor's fallback: taskId is a session_id recorded in
	// <dir>/<dir>.json (session_id != dirname). The shim must find it.
	home := t.TempDir()
	t.Setenv("HOME", home)        // os.homedir() in node reads HOME on unix
	t.Setenv("USERPROFILE", home) // os.UserHomeDir reads this on Windows
	sessDir := filepath.Join(home, ".cline", "data", "sessions", "realdir")
	if err := os.MkdirAll(sessDir, 0755); err != nil {
		t.Fatal(err)
	}
	meta := `{"session_id":"task-xyz","messages_path":"` + sessDir + `/realdir.messages.json"}`
	if err := os.WriteFile(filepath.Join(sessDir, "realdir.json"), []byte(meta), 0644); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	stub := stubPalateScript(t, dir)
	stdinFile := filepath.Join(dir, "stdin.json")
	t.Setenv("STUB_STDIN_FILE", stdinFile)

	shim := filepath.Join(dir, "TaskStart.mjs")
	if err := os.WriteFile(shim, []byte(GenerateClineShim("TaskStart", "session-start", stub)), 0755); err != nil {
		t.Fatal(err)
	}
	runShim(t, shim, `{"taskId":"task-xyz","workspaceRoots":["/w"],"taskStart":{"taskMetadata":{}}}`)
	assertForwardedPayload(t, stdinFile, `{"session_id":"task-xyz","transcript_path":"`+sessDir+`/realdir.messages.json","cwd":"/w","source":"startup"}`)
}

func TestWriteClineShimsMergesAndPreserves(t *testing.T) {
	dir := t.TempDir()
	// Foreign files: the operator's own hooks must survive install+uninstall.
	foreign := map[string]string{
		"TaskStart":            "#!/bin/sh\necho foreign\n",
		"UserPromptSubmit.cjs": "console.log('foreign cjs');",
		"PostToolUse":          "#!/usr/bin/env node\n/* bridge */",
		"notes.txt":            "not a hook at all",
	}
	for name, body := range foreign {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0755); err != nil {
			t.Fatal(err)
		}
	}

	if n := CountClineHooks(dir); n != 0 {
		t.Fatalf("CountClineHooks on foreign-only dir = %d, want 0", n)
	}

	// A foreign file occupying exactly <Event>.mjs must make the install fail
	// LOUDLY — Cline fires every file matching an event name, so overwriting
	// it would silently kill a foreign hook.
	conflict := filepath.Join(dir, "UserPromptSubmit.mjs")
	if err := os.WriteFile(conflict, []byte("console.log('foreign mjs');"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := writeClineShimsAt(dir, "/opt/palate/palate"); err == nil {
		t.Fatal("install succeeded over a foreign <Event>.mjs file — must fail with a conflict")
	}
	if got, _ := os.ReadFile(conflict); string(got) != "console.log('foreign mjs');" {
		t.Fatal("conflicting foreign file was mutated by the failed install")
	}
	if n := CountClineHooks(dir); n != 0 {
		t.Fatalf("failed install left %d shims behind (must be all-or-nothing)", n)
	}
	if err := os.Remove(conflict); err != nil {
		t.Fatal(err)
	}

	if _, err := writeClineShimsAt(dir, "/opt/palate/palate"); err != nil {
		t.Fatalf("install: %v", err)
	}
	if n := CountClineHooks(dir); n != len(clineShims) {
		t.Fatalf("after install CountClineHooks = %d, want %d", n, len(clineShims))
	}
	// Idempotent: a second install must not duplicate or corrupt.
	if _, err := writeClineShimsAt(dir, "/opt/palate/palate"); err != nil {
		t.Fatalf("second install: %v", err)
	}
	if n := CountClineHooks(dir); n != len(clineShims) {
		t.Fatalf("after reinstall CountClineHooks = %d, want %d", n, len(clineShims))
	}

	for name, body := range foreign {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("foreign file %s lost: %v", name, err)
		}
		if string(got) != body {
			t.Fatalf("foreign file %s mutated", name)
		}
	}
	if !isPalateClineShim(filepath.Join(dir, "TaskStart.mjs")) {
		t.Fatal("palate TaskStart.mjs not recognized")
	}
	if isPalateClineShim(filepath.Join(dir, "notes.txt")) {
		t.Fatal("notes.txt misrecognized")
	}

	removed, err := RemoveClineHooks(dir)
	if err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if removed != len(clineShims) {
		t.Fatalf("uninstall removed %d, want %d", removed, len(clineShims))
	}
	if n := CountClineHooks(dir); n != 0 {
		t.Fatalf("after uninstall CountClineHooks = %d, want 0", n)
	}
	for name := range foreign {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("foreign file %s removed by uninstall", name)
		}
	}
}

func TestRemoveClineHooksAbsentDir(t *testing.T) {
	removed, err := RemoveClineHooks(filepath.Join(t.TempDir(), "nope"))
	if err != nil || removed != 0 {
		t.Fatalf("RemoveClineHooks on absent dir = (%d, %v), want (0, nil)", removed, err)
	}
}
