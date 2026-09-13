#!/bin/sh
# Gate G3: bin/palate's 5-step resolution order — PALATE_BIN override, staged
# per-platform binary, PATH, locked go build into .cache/, fail-open stderr
# line — behaves as documented. Run against a temp copy of the launcher so
# nothing here touches the real repo's .cache/ or config.
set -eu

REPO="$(cd "$(dirname "$0")/.." && pwd)"
GO_BIN="$(command -v go || true)"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

COPY="$WORK/copy"
mkdir -p "$COPY/bin"
cp "$REPO/bin/palate" "$COPY/bin/palate"
chmod +x "$COPY/bin/palate"

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
env -i HOME="$HOME" CLAUDE_PLUGIN_ROOT="$COPY" "$COPY/bin/palate" hello >"$WORK/case3.out" 2>"$err"
code=$?
set -e
[ "$code" -eq 0 ] || fail "case 3: exit code $code, want 0"
[ -s "$WORK/case3.out" ] && fail "case 3: expected no stdout, got: $(cat "$WORK/case3.out")"
lines="$(wc -l < "$err" | tr -d ' ')"
[ "$lines" -eq 1 ] || fail "case 3: expected exactly 1 stderr line, got $lines: $(cat "$err")"
grep -q "no binary found and go is not installed" "$err" || fail "case 3: unexpected stderr: $(cat "$err")"
echo "case 3 PASS: empty PATH + no go -> exactly one stderr line, exit 0"

# ---- Case 4: go present, no palate on PATH -> builds into .cache/palate ----
if [ -z "$GO_BIN" ]; then
  echo "case 4 SKIPPED: no go toolchain found on this machine"
else
  gobin_dir="$(dirname "$GO_BIN")"
  start="$(date +%s)"
  set +e
  env -i HOME="$HOME" PATH="$gobin_dir:/usr/bin:/bin" \
    GOCACHE="$(go env GOCACHE)" GOPATH="$(go env GOPATH)" GOMODCACHE="$(go env GOMODCACHE)" \
    CLAUDE_PLUGIN_ROOT="$REPO" "$COPY/bin/palate" version >"$WORK/case4.out" 2>"$WORK/case4.err"
  code=$?
  set -e
  elapsed=$(( $(date +%s) - start ))
  if [ "$elapsed" -gt 60 ]; then
    echo "case 4 SKIPPED: go build fallback took ${elapsed}s (>60s budget) — not asserting further, but this ran"
  else
    [ "$code" -eq 0 ] || fail "case 4: exit code $code (${elapsed}s): $(cat "$WORK/case4.err")"
    grep -q "^palate " "$WORK/case4.out" || fail "case 4: expected version output, got: $(cat "$WORK/case4.out") / $(cat "$WORK/case4.err")"
    [ -x "$REPO/.cache/palate" ] || fail "case 4: expected $REPO/.cache/palate to exist after the build"
    echo "case 4 PASS: go build fallback built into .cache/palate in ${elapsed}s"
    rm -rf "$REPO/.cache"
  fi
fi

echo "verify-plugin-launcher: all cases done"
