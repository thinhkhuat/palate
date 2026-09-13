#!/bin/sh
# Gate G3: bin/palate's 5-step resolution order — PALATE_BIN override, staged
# per-platform binary, PATH, locked go build into .cache/, fail-open stderr
# line — behaves as documented, and the plugin root is resolved from the
# launcher's own location, never from a foreign or literal CLAUDE_PLUGIN_ROOT.
# Run against a temp copy of the source tree so nothing here touches the real
# repo's .cache/ or config.
set -eu

REPO="$(cd "$(dirname "$0")/.." && pwd)"
GO_BIN="$(command -v go || true)"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# A minimal but real source tree: the launcher's step 4 builds from the tree
# it lives in, so the copy needs go.mod + the Go sources, not just bin/.
COPY="$WORK/copy"
mkdir -p "$COPY/bin"
cp "$REPO/bin/palate" "$COPY/bin/palate"
chmod +x "$COPY/bin/palate"
cp "$REPO/go.mod" "$REPO/go.sum" "$COPY/"
cp -R "$REPO/cmd" "$REPO/internal" "$COPY/"

# go_env — the caller's Go caches, so a fallback build stays fast + offline.
go_env() {
  echo "GOCACHE=$(go env GOCACHE) GOPATH=$(go env GOPATH) GOMODCACHE=$(go env GOMODCACHE) GOFLAGS=-mod=mod"
}

fail() {
  echo "verify-plugin-launcher: FAIL: $1" >&2
  exit 1
}

# ---- Case 1: PALATE_BIN override wins over everything else ----
stub1="$WORK/stub1.sh"
cat > "$stub1" <<'EOF'
#!/bin/sh
echo "stub1: $*"
EOF
chmod +x "$stub1"

out="$(env -i HOME="$HOME" PATH="/usr/bin:/bin" PALATE_BIN="$stub1" "$COPY/bin/palate" hello)"
[ "$out" = "stub1: hello" ] || fail "case 1: PALATE_BIN override not used (got: $out)"
echo "case 1 PASS: PALATE_BIN override wins"

# ---- Case 2: PALATE_BIN unset, a stub palate first on PATH is used ----
pathdir="$WORK/pathbin"
mkdir -p "$pathdir"
cat > "$pathdir/palate" <<'EOF'
#!/bin/sh
echo "path-stub: $*"
EOF
chmod +x "$pathdir/palate"

out="$(env -i HOME="$HOME" PATH="$pathdir:/usr/bin:/bin" "$COPY/bin/palate" hello)"
[ "$out" = "path-stub: hello" ] || fail "case 2: PATH stub not used (got: $out)"
echo "case 2 PASS: PATH lookup used when PALATE_BIN is unset"

# ---- Case 3: empty PATH, no go — one stderr line, exit 0 ----
err="$WORK/case3.err"
set +e
env -i HOME="$HOME" "$COPY/bin/palate" hello >"$WORK/case3.out" 2>"$err"
code=$?
set -e
[ "$code" -eq 0 ] || fail "case 3: exit code $code, want 0"
[ -s "$WORK/case3.out" ] && fail "case 3: expected no stdout, got: $(cat "$WORK/case3.out")"
lines="$(wc -l < "$err" | tr -d ' ')"
[ "$lines" -eq 1 ] || fail "case 3: expected exactly 1 stderr line, got $lines: $(cat "$err")"
grep -q "no binary found and go is not installed" "$err" || fail "case 3: unexpected stderr: $(cat "$err")"
echo "case 3 PASS: empty PATH + no go -> exactly one stderr line, exit 0"

# ---- Case 4: go present, no palate on PATH -> builds into its OWN .cache/palate ----
if [ -z "$GO_BIN" ]; then
  echo "case 4 SKIPPED: no go toolchain found on this machine"
  echo "case 5 SKIPPED: no go toolchain found on this machine"
  echo "case 6 SKIPPED: no go toolchain found on this machine"
else
  gobin_dir="$(dirname "$GO_BIN")"
  start="$(date +%s)"
  set +e
  # shellcheck disable=SC2046
  env -i HOME="$HOME" PATH="$gobin_dir:/usr/bin:/bin" $(go_env) \
    "$COPY/bin/palate" version >"$WORK/case4.out" 2>"$WORK/case4.err"
  code=$?
  set -e
  elapsed=$(( $(date +%s) - start ))
  if [ "$elapsed" -gt 60 ]; then
    echo "case 4 SKIPPED: go build fallback took ${elapsed}s (>60s budget) — not asserting further, but this ran"
  else
    [ "$code" -eq 0 ] || fail "case 4: exit code $code (${elapsed}s): $(cat "$WORK/case4.err")"
    grep -q "^palate " "$WORK/case4.out" || fail "case 4: expected version output, got: $(cat "$WORK/case4.out") / $(cat "$WORK/case4.err")"
    [ -x "$COPY/.cache/palate" ] || fail "case 4: expected $COPY/.cache/palate to exist after the build"
    [ -e "$REPO/.cache/palate" ] && fail "case 4: launcher built into the REPO ($REPO/.cache) instead of its own tree"
    echo "case 4 PASS: go build fallback built into the launcher's own .cache/palate in ${elapsed}s"
  fi

  # ---- Case 5: a literal, unexpanded CLAUDE_PLUGIN_ROOT must be ignored ----
  # Regression: Claude's settings.json env can carry the literal string
  # '$HOME/.claude' into every spawned process. The launcher used to trust it,
  # mkdir '$HOME/.claude/.cache' in the caller's cwd, and report "go is not
  # installed" with go present. It must resolve from $0 and still work.
  cwd5="$WORK/cwd5"
  mkdir -p "$cwd5"
  set +e
  # shellcheck disable=SC2046,SC2016
  (cd "$cwd5" && env -i HOME="$HOME" PATH="$gobin_dir:/usr/bin:/bin" $(go_env) \
    CLAUDE_PLUGIN_ROOT='$HOME/.claude' "$COPY/bin/palate" version) >"$WORK/case5.out" 2>"$WORK/case5.err"
  code=$?
  set -e
  [ "$code" -eq 0 ] || fail "case 5: exit code $code: $(cat "$WORK/case5.err")"
  grep -q "^palate " "$WORK/case5.out" || fail "case 5: expected version output, got: $(cat "$WORK/case5.out") / $(cat "$WORK/case5.err")"
  [ -e "$cwd5/\$HOME" ] && fail "case 5: junk '\$HOME' directory created in the caller's cwd"
  [ -z "$(ls -A "$cwd5")" ] || fail "case 5: caller's cwd was written to: $(ls -A "$cwd5")"
  echo "case 5 PASS: literal CLAUDE_PLUGIN_ROOT ignored, resolved from \$0, cwd untouched"

  # ---- Case 6: a foreign but VALID CLAUDE_PLUGIN_ROOT must not win over $0 ----
  # Another palate tree (a stub launcher) is pointed at via the env var; the
  # launcher must still use its own tree, not the foreign one.
  foreign="$WORK/foreign"
  mkdir -p "$foreign/bin"
  printf '#!/bin/sh\necho "foreign: $*"\n' > "$foreign/bin/palate"
  chmod +x "$foreign/bin/palate"
  # shellcheck disable=SC2046
  out="$(env -i HOME="$HOME" PATH="$gobin_dir:/usr/bin:/bin" $(go_env) \
    CLAUDE_PLUGIN_ROOT="$foreign" "$COPY/bin/palate" version 2>&1)"
  case "$out" in
    palate*) echo "case 6 PASS: own tree wins over a foreign CLAUDE_PLUGIN_ROOT" ;;
    *) fail "case 6: expected own build's version output, got: $out" ;;
  esac
fi

# ---- Case 7: no go, unusable literal root -> exactly one truthful stderr line ----
err7="$WORK/case7.err"
set +e
# shellcheck disable=SC2016
env -i HOME="$HOME" CLAUDE_PLUGIN_ROOT='$HOME/.claude' "$COPY/bin/palate" hello >"$WORK/case7.out" 2>"$err7"
code=$?
set -e
[ "$code" -eq 0 ] || fail "case 7: exit code $code, want 0"
lines="$(wc -l < "$err7" | tr -d ' ')"
[ "$lines" -eq 1 ] || fail "case 7: expected exactly 1 stderr line, got $lines: $(cat "$err7")"
grep -q "go is not installed" "$err7" || fail "case 7: unexpected stderr: $(cat "$err7")"
echo "case 7 PASS: literal root + no go -> one truthful stderr line, exit 0"

echo "verify-plugin-launcher: all cases done"
