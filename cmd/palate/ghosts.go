package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ghostMinAge is how long an orphaned launcher must have lived before serve
// reaps it. A healthy launcher execs within milliseconds; its slow paths
// (go build, the build-lock wait) always have a child, which launcherGhosts
// excludes. Ten minutes is margin, not the safety mechanism.
const ghostMinAge = 10 * time.Minute

type psRow struct {
	pid, ppid, uid int
	age            time.Duration
	cmd            string
}

// isLauncherGhost reports whether row is an orphaned `bin/palate serve`
// plugin launcher: still the shell (it never reached exec), its session gone
// (reparented to init), owned by us, and old enough that it never will exec.
// Observed 2026-09-26: four `/bin/sh …/palate/0.5.0/bin/palate serve` shells,
// PPID 1, no children, 13-14h old, killed by SIGTERM (so sleeping, not
// stopped). Why they stuck before exec is unproven — reapLauncherGhosts logs
// each one's state first so the next occurrence names the cause.
func isLauncherGhost(r psRow, uid int, minAge time.Duration) bool {
	if r.ppid != 1 || r.uid != uid || r.age < minAge || !strings.HasSuffix(r.cmd, "/bin/palate serve") {
		return false
	}
	// bin/palate's `#!/bin/sh` makes the kernel run it as `/bin/sh <path> serve`.
	shell, _, _ := strings.Cut(r.cmd, " ")
	return filepath.Base(shell) == "sh"
}

// parsePS parses `ps -A -o pid=,ppid=,uid=,etime=,command=` output, skipping
// any row it cannot read.
func parsePS(out string) []psRow {
	var rows []psRow
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 5 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		uid, err3 := strconv.Atoi(f[2])
		age, err4 := parseEtime(f[3])
		if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
			continue
		}
		rows = append(rows, psRow{pid, ppid, uid, age, strings.Join(f[4:], " ")})
	}
	return rows
}

// parseEtime parses ps's elapsed time, [[dd-]hh:]mm:ss.
func parseEtime(s string) (time.Duration, error) {
	days := 0
	if d, rest, ok := strings.Cut(s, "-"); ok {
		n, err := strconv.Atoi(d)
		if err != nil {
			return 0, err
		}
		days, s = n, rest
	}
	secs := 0
	for _, p := range strings.Split(s, ":") {
		n, err := strconv.Atoi(p)
		if err != nil {
			return 0, err
		}
		secs = secs*60 + n
	}
	return time.Duration(days)*24*time.Hour + time.Duration(secs)*time.Second, nil
}

// launcherGhosts returns the rows that are launcher ghosts. A launcher with a
// child is never one: that is bin/palate step 4 mid `go build`, and stopping
// it would strand .cache/build.lock and break every later launch.
func launcherGhosts(rows []psRow, uid int, minAge time.Duration) []psRow {
	hasChild := map[int]bool{}
	for _, r := range rows {
		hasChild[r.ppid] = true
	}
	var ghosts []psRow
	for _, r := range rows {
		if isLauncherGhost(r, uid, minAge) && !hasChild[r.pid] {
			ghosts = append(ghosts, r)
		}
	}
	return ghosts
}

// scanGhosts lists this user's launcher ghosts from a fresh `ps` snapshot.
func scanGhosts(minAge time.Duration) []psRow {
	out, err := exec.Command("ps", "-A", "-o", "pid=,ppid=,uid=,etime=,command=").Output()
	if err != nil {
		return nil
	}
	return launcherGhosts(parsePS(string(out)), os.Getuid(), minAge)
}

// reapLauncherGhosts stops every orphaned palate launcher older than minAge,
// appending its ps row, open files and (macOS) stack sample to logPath first.
// Best-effort and silent: serve's stdout is the MCP channel, so nothing here
// may print, and no failure here may affect serving.
func reapLauncherGhosts(logPath string, minAge time.Duration) {
	// A panic here must not take serve down, but must not hide either.
	defer func() {
		if v := recover(); v != nil {
			appendLog(logPath, fmt.Sprintf("=== %s reaper panic: %v\n", time.Now().Format(time.RFC3339), v))
		}
	}()
	if runtime.GOOS == "windows" {
		return
	}
	for _, g := range scanGhosts(minAge) {
		recordGhost(logPath, g)
		// Recording takes a second or more; re-check so a pid reused in the
		// meantime by an unrelated process is never signalled.
		for _, now := range scanGhosts(minAge) {
			if now.pid == g.pid && now.cmd == g.cmd {
				if p, err := os.FindProcess(g.pid); err == nil {
					_ = p.Signal(syscall.SIGTERM)
				}
			}
		}
	}
}

func recordGhost(logPath string, r psRow) {
	pid := strconv.Itoa(r.pid)
	var b strings.Builder
	fmt.Fprintf(&b, "=== %s reaped launcher ghost pid=%d age=%s by serve pid=%d\n%s\n",
		time.Now().Format(time.RFC3339), r.pid, r.age, os.Getpid(), r.cmd)
	probes := [][]string{
		{"ps", "-o", "pid,ppid,pgid,stat,lstart,command", "-p", pid},
		{"lsof", "-p", pid},
	}
	if runtime.GOOS == "darwin" {
		probes = append(probes, []string{"/usr/bin/sample", pid, "1"})
	}
	for _, p := range probes {
		out, err := exec.Command(p[0], p[1:]...).CombinedOutput()
		fmt.Fprintf(&b, "--- %s (err: %v)\n%s", strings.Join(p, " "), err, out)
		// sample also leaves a copy in /tmp; the log above already holds it.
		if _, tmp, ok := strings.Cut(string(out), " written to file "); ok {
			tmp, _, _ = strings.Cut(tmp, "\n")
			_ = os.Remove(tmp)
		}
	}
	appendLog(logPath, b.String())
}

func appendLog(logPath, text string) {
	if os.MkdirAll(filepath.Dir(logPath), 0700) != nil {
		return
	}
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(text)
}
