#!/bin/sh
# Gate G5: the two MCP descriptor invocation forms — .mcp.json's
# ${CLAUDE_PLUGIN_ROOT}-rooted command (Claude Code) and
# .codex-plugin/mcp.json's relative ./bin/palate with cwd:"." (Codex,
# ADR-0035) — must both answer tools/list with the same 8 tools.
set -eu

REPO="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

BIN="$WORK/palate-verify"
(cd "$REPO" && CGO_ENABLED=0 go build -o "$BIN" ./cmd/palate)

unset PALATE_LLM_ENDPOINT PALATE_LLM_MODEL PALATE_LLM_API_KEY 2>/dev/null || true

# Fresh config/DB per form so both start from identical, empty state — each
# form gets its own isolated home via PALATE_HOME, so nothing here touches
# the developer's real ~/.claude/palate.
unset PALATE_HOME 2>/dev/null || true
WA="$(mktemp -d)"
WB="$(mktemp -d)"
(cd "$WA" && PALATE_HOME="$WA/home" "$BIN" init >/dev/null)
(cd "$WB" && PALATE_HOME="$WB/home" "$BIN" init >/dev/null)

# rpc_seq — the initialize -> notifications/initialized -> tools/list
# handshake, id 2 on the tools/list request; sleeps 1s before closing stdin.
rpc_seq() {
  printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"verify-plugin-mcp","version":"0.0.0"}}}'
  printf '%s\n' '{"jsonrpc":"2.0","method":"notifications/initialized"}'
  printf '%s\n' '{"jsonrpc":"2.0","id":2,"method":"tools/list"}'
  sleep 1
}

# tool_names <raw-jsonrpc-output> — extracts the id:2 result's sorted tool
# names, comma-joined, via node -e (not jq).
tool_names() {
  node -e '
const fs = require("fs");
const lines = fs.readFileSync(0, "utf8").split("\n").filter(Boolean);
let names = null;
for (const line of lines) {
  let msg;
  try { msg = JSON.parse(line); } catch { continue; }
  if (msg.id === 2 && msg.result && Array.isArray(msg.result.tools)) {
    names = msg.result.tools.map(t => t.name).sort();
  }
}
if (!names) { process.stderr.write("no tools/list result found\n"); process.exit(1); }
process.stdout.write(names.join(","));
'
}

# Form A: .mcp.json's shape — ${CLAUDE_PLUGIN_ROOT}/bin/palate serve.
outA="$WORK/mcp.launcher.raw"
(
  cd "$WA"
  CLAUDE_PLUGIN_ROOT="$REPO"
  export CLAUDE_PLUGIN_ROOT
  PALATE_BIN="$BIN"
  export PALATE_BIN
  PALATE_HOME="$WA/home"
  export PALATE_HOME
  rpc_seq | sh -c '"${CLAUDE_PLUGIN_ROOT}/bin/palate" serve'
) > "$outA"

# Form B: .codex-plugin/mcp.json's shape — relative ./bin/palate, cwd ".".
# CLAUDE_PLUGIN_ROOT is left unset so bin/palate self-locates via $0, the
# way Codex's MCP spawn actually runs it.
outB="$WORK/mcp.relative.raw"
(
  cd "$REPO"
  PALATE_BIN="$BIN"
  export PALATE_BIN
  unset CLAUDE_PLUGIN_ROOT 2>/dev/null || true
  # cwd is the repo (which carries its own legacy .palate/); PALATE_HOME must
  # still win, so this form also proves the override precedence.
  PALATE_HOME="$WB/home"
  export PALATE_HOME
  rpc_seq | ./bin/palate serve
) > "$outB"

namesA="$(tool_names < "$outA")"
namesB="$(tool_names < "$outB")"

expected="palate_accept,palate_correct,palate_forget,palate_prefer,palate_recall,palate_reject,palate_remember,palate_stats"

status=0
if [ "$namesA" != "$expected" ]; then
  echo "form A (\${CLAUDE_PLUGIN_ROOT}/bin/palate serve) tools mismatch:" >&2
  echo "  got:      $namesA" >&2
  echo "  expected: $expected" >&2
  status=1
fi
if [ "$namesB" != "$expected" ]; then
  echo "form B (cd <repo> && ./bin/palate serve) tools mismatch:" >&2
  echo "  got:      $namesB" >&2
  echo "  expected: $expected" >&2
  status=1
fi

rm -rf "$WA" "$WB"

# Legacy pass: a cwd that already carries ./.palate/palate.db keeps the
# per-repo store. Form B needs the launcher beside it (self-locates via $0),
# so a throwaway tree gets a copy of bin/ — never the repo's own .palate/.
WL="$(mktemp -d)"
mkdir -p "$WL/.palate" "$WL/bin" && : > "$WL/.palate/palate.db"
cp "$REPO/bin/palate" "$WL/bin/palate" && chmod +x "$WL/bin/palate"
outL="$WORK/mcp.legacy.raw"
(
  cd "$WL"
  PALATE_BIN="$BIN"
  export PALATE_BIN
  unset CLAUDE_PLUGIN_ROOT PALATE_HOME 2>/dev/null || true
  rpc_seq | ./bin/palate serve
) > "$outL"
namesL="$(tool_names < "$outL")"
if [ "$namesL" != "$expected" ]; then
  echo "legacy form (cwd with .palate/palate.db) tools mismatch:" >&2
  echo "  got:      $namesL" >&2
  status=1
fi
[ -d "$WL/home" ] && { echo "legacy form created a home dir" >&2; status=1; }
[ -f "$WL/.palate/palate.db" ] || { echo "legacy form lost .palate/palate.db" >&2; status=1; }
rm -rf "$WL"

if [ "$status" -ne 0 ]; then
  echo "verify-plugin-mcp: FAILED" >&2
  exit 1
fi

echo "verify-plugin-mcp: all three forms (plugin-root, relative cwd, legacy repo) answer tools/list with the same 8 tools"
