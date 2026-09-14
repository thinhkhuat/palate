#!/bin/sh
# Gate G4: every command in hooks/hooks.json, invoked the way a plugin
# install would invoke it (CLAUDE_PLUGIN_ROOT expanded), must produce
# byte-identical stdout to the direct `palate hook <event>` call, on each of
# five reference payloads, in a freshly `palate init`-ed workspace.
#
# PALATE_LLM_* stays unset throughout so no child distiller process spawns.
set -eu

REPO="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

BIN="$WORK/palate-verify"
(cd "$REPO" && CGO_ENABLED=0 go build -o "$BIN" ./cmd/palate)

unset PALATE_LLM_ENDPOINT PALATE_LLM_MODEL PALATE_LLM_API_KEY 2>/dev/null || true

# Both store layouts are exercised, and neither may touch the developer's
# real ~/.claude/palate: "global" pins an isolated home per form through
# PALATE_HOME (the same override a user would set); "legacy" plants the
# ./.palate/palate.db marker so detection keeps the per-repo layout.
unset PALATE_HOME 2>/dev/null || true
prep_dir() {  # prep_dir <dir> <mode>
  if [ "$2" = "legacy" ]; then
    mkdir -p "$1/.palate" && : > "$1/.palate/palate.db"
    (cd "$1" && "$BIN" init >/dev/null)
  else
    (cd "$1" && PALATE_HOME="$1/home" "$BIN" init >/dev/null)
  fi
}
home_env() {  # home_env <dir> <mode> — prints the env assignment for that form
  if [ "$2" = "global" ]; then printf 'PALATE_HOME=%s/home' "$1"; else printf 'PALATE_HOME='; fi
}

# Extract every hook command from hooks/hooks.json with node -e (not jq),
# one "<Event>\t<command>" line per hook entry.
extract_hooks() {
  node -e '
const fs = require("fs");
const data = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
for (const [event, entries] of Object.entries(data.hooks)) {
  for (const entry of entries) {
    for (const h of entry.hooks) {
      process.stdout.write(event + "\t" + h.command + "\n");
    }
  }
}
' "$REPO/hooks/hooks.json"
}

# payload_for <cli-event> <workspace> — one of the five reference payloads,
# with <W> substituted for the given workspace path.
payload_for() {
  case "$1" in
    session-start)
      printf '{"session_id":"s1","transcript_path":"/tmp/t.jsonl","cwd":"%s","hook_event_name":"SessionStart","source":"startup"}' "$2"
      ;;
    user-prompt)
      printf '{"session_id":"s1","hook_event_name":"UserPromptSubmit","prompt":"I prefer snake_case for python function names. now write the tests"}'
      ;;
    post-tool-use)
      printf '{"session_id":"s1","cwd":"%s","hook_event_name":"PostToolUse","tool_name":"Write","tool_input":{"file_path":"%s/calc.go","content":"x"},"tool_response":{"filePath":"%s/calc.go","success":true}}' "$2" "$2" "$2"
      ;;
    stop)
      printf '{"session_id":"s1","cwd":"%s","hook_event_name":"Stop","stop_hook_active":false}' "$2"
      ;;
    pre-compact)
      printf '{"session_id":"s1","cwd":"%s","hook_event_name":"PreCompact","trigger":"auto","custom_instructions":""}' "$2"
      ;;
    *)
      echo "verify-plugin-hooks: unknown event slug $1" >&2
      exit 1
      ;;
  esac
}

# cli_event_for <ClaudeCode-event-name> — maps the JSON event name to the CLI
# subcommand slug the extracted command already ends in (used only to pick
# the right reference payload / fixture).
cli_event_for() {
  case "$1" in
    SessionStart) echo session-start ;;
    UserPromptSubmit) echo user-prompt ;;
    PostToolUse) echo post-tool-use ;;
    Stop) echo stop ;;
    PreCompact) echo pre-compact ;;
    *) echo "verify-plugin-hooks: unknown hooks.json event $1" >&2; exit 1 ;;
  esac
}

status=0
for MODE in global legacy; do
extract_hooks | while IFS="$(printf '\t')" read -r event cmd; do
  slug="$(cli_event_for "$event")"

  WA="$(mktemp -d)"
  WB="$(mktemp -d)"
  prep_dir "$WA" "$MODE"
  prep_dir "$WB" "$MODE"

  if [ "$slug" = "post-tool-use" ]; then
    printf 'x' > "$WA/calc.go"
    printf 'x' > "$WB/calc.go"
  fi

  payloadA="$(payload_for "$slug" "$WA")"
  payloadB="$(payload_for "$slug" "$WB")"

  outA="$WORK/out.$slug.launcher"
  outB="$WORK/out.$slug.direct"

  # Form A: exactly what hooks.json ships — the ${CLAUDE_PLUGIN_ROOT}-rooted
  # command, expanded by a shell the way a harness invokes it. Env vars must
  # be exported in the subshell (not prefixed on the pipeline's first
  # command only) so `sh -c "$cmd"`, the pipeline's second stage, sees them —
  # this also overrides any ambient CLAUDE_PLUGIN_ROOT already inherited from
  # the caller's own environment.
  (
    cd "$WA"
    CLAUDE_PLUGIN_ROOT="$REPO"
    export CLAUDE_PLUGIN_ROOT
    PALATE_BIN="$BIN"
    export PALATE_BIN
    export "$(home_env "$WA" "$MODE")"
    printf '%s' "$payloadA" | sh -c "$cmd"
  ) > "$outA"

  # Form B: the direct CLI call the launcher must be indistinguishable from.
  (cd "$WB" && export "$(home_env "$WB" "$MODE")" && printf '%s' "$payloadB" | "$BIN" hook "$slug") > "$outB"

  # Each form ran in its own temp dir and the output may name that dir (the
  # profile path in the SessionStart block); mask it before comparing.
  sed "s|$WA|<WORK>|g" "$outA" > "$outA.n" && mv "$outA.n" "$outA"
  sed "s|$WB|<WORK>|g" "$outB" > "$outB.n" && mv "$outB.n" "$outB"
  if ! cmp -s "$outA" "$outB"; then
    echo "DRIFT on $event ($slug) [$MODE]:" >&2
    diff -u "$outB" "$outA" >&2 || true
    rm -rf "$WA" "$WB"
    exit 1
  fi
  # the store must be where the mode says, and nowhere else
  if [ "$MODE" = "global" ]; then
    [ -e "$WA/.palate" ] && { echo "global mode wrote $WA/.palate" >&2; exit 1; }
    [ -f "$WA/home/palate.db" ] || { echo "global mode: no DB under PALATE_HOME" >&2; exit 1; }
  else
    [ -d "$WA/home" ] && { echo "legacy mode created a home dir" >&2; exit 1; }
    [ -f "$WA/.palate/palate.db" ] || { echo "legacy mode lost .palate/palate.db" >&2; exit 1; }
  fi
  rm -rf "$WA" "$WB"
done || status=1
done

if [ "$status" -ne 0 ]; then
  echo "verify-plugin-hooks: FAILED" >&2
  exit 1
fi

echo "verify-plugin-hooks: all hooks.json commands byte-match direct CLI calls (global + legacy store layouts)"
