package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestParseEtime(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"00:05":       5 * time.Second,
		"13:51:44":    13*time.Hour + 51*time.Minute + 44*time.Second,
		"2-01:00:00":  49 * time.Hour,
		"10-00:00:01": 240*time.Hour + time.Second,
	} {
		if got, err := parseEtime(in); err != nil || got != want {
			t.Errorf("parseEtime(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := parseEtime("x:01"); err == nil {
		t.Error("parseEtime accepted garbage")
	}
}

func TestIsLauncherGhost(t *testing.T) {
	const uid = 501
	ghost := "/bin/sh /Users/u/.claude/plugins/cache/palate/palate/0.5.0/bin/palate serve"
	old := 14 * time.Hour
	for _, c := range []struct {
		name string
		row  psRow
		want bool
	}{
		{"observed ghost", psRow{41985, 1, uid, old, ghost}, true},
		{"run via bash, not the shebang", psRow{1, 1, uid, old, "/bin/bash /opt/p/bin/palate serve"}, false},
		{"live session parent", psRow{1, 69856, uid, old, ghost}, false},
		{"another user's", psRow{1, 1, 0, old, ghost}, false},
		{"young (may still exec)", psRow{1, 1, uid, time.Minute, ghost}, false},
		{"healthy exec'd server", psRow{1, 1, uid, old, "palate serve"}, false},
		{"hook launcher, not serve", psRow{1, 1, uid, old, "/bin/sh /p/bin/palate hook session-start"}, false},
		{"not a shell", psRow{1, 1, uid, old, "/usr/bin/python3 /p/bin/palate serve"}, false},
		{"other tool's launcher", psRow{1, 1, uid, old, "/bin/sh /p/bin/notpalate serve"}, false},
	} {
		if got := isLauncherGhost(c.row, uid, ghostMinAge); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestLauncherGhostsSkipsBuildingLauncher(t *testing.T) {
	const uid = 501
	old := 14 * time.Hour
	cmd := "/bin/sh /p/bin/palate serve"
	rows := []psRow{
		{100, 1, uid, old, cmd}, // stuck, no children: ghost
		{200, 1, uid, old, cmd}, // orphaned mid `go build`
		{201, 200, uid, old, "go build -o x ./cmd/palate"},
	}
	got := launcherGhosts(rows, uid, ghostMinAge)
	if len(got) != 1 || got[0].pid != 100 {
		t.Fatalf("got %+v, want only pid 100", got)
	}
}

func TestParsePS(t *testing.T) {
	out := "    1     0     0 02:16:07 /sbin/launchd\n" +
		"41985     1   501 13:51:44 /bin/sh /a b/bin/palate serve\n" +
		"garbage line\n"
	rows := parsePS(out)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2: %+v", len(rows), rows)
	}
	r := rows[1]
	if r.pid != 41985 || r.ppid != 1 || r.uid != 501 || r.cmd != "/bin/sh /a b/bin/palate serve" {
		t.Errorf("row parsed wrong: %+v", r)
	}
}

// TestReapLauncherGhostsRealOrphan builds the observed ghost for real: a
// launcher named …/bin/palate, orphaned onto init, blocked with no children
// before any exec. The reaper must log it and stop it via the real ps.
func TestReapLauncherGhostsRealOrphan(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("launcher is POSIX sh")
	}
	dir := t.TempDir()
	fifo := filepath.Join(dir, "never-written")
	if out, err := exec.Command("mkfifo", fifo).CombinedOutput(); err != nil {
		t.Fatalf("mkfifo: %v %s", err, out)
	}
	launcher := filepath.Join(dir, "bin", "palate")
	if err := os.MkdirAll(filepath.Dir(launcher), 0700); err != nil {
		t.Fatal(err)
	}
	// open(2) on a FIFO with no writer blocks forever, in the shell itself.
	script := "#!/bin/sh\nread x < '" + fifo + "'\n"
	if err := os.WriteFile(launcher, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	// The intermediate shell exits at once, orphaning the launcher.
	out, err := exec.Command("/bin/sh", "-c", `"$0" serve >/dev/null 2>&1 & echo $!`, launcher).Output()
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatal(err)
	}
	proc, _ := os.FindProcess(pid)
	t.Cleanup(func() { _ = proc.Signal(syscall.SIGKILL) })

	var orphaned bool
	for i := 0; i < 50 && !orphaned; i++ {
		time.Sleep(100 * time.Millisecond)
		ps, _ := exec.Command("ps", "-o", "ppid=,command=", "-p", strconv.Itoa(pid)).Output()
		f := strings.Fields(string(ps))
		orphaned = len(f) > 1 && f[0] == "1" && strings.HasSuffix(string(ps), "/bin/palate serve\n")
	}
	if !orphaned {
		t.Skip("launcher was not reparented to init (subreaper host?); ghost shape not reproducible here")
	}

	// minAge 0 so the fresh fixture qualifies. This is the real, machine-wide
	// reaper: it also stops any other childless orphaned palate launcher on
	// this box — each such one has no session left to serve.
	logPath := filepath.Join(dir, "state", "launcher-ghosts.log")
	reapLauncherGhosts(logPath, 0)

	gone := false
	for i := 0; i < 50 && !gone; i++ {
		time.Sleep(100 * time.Millisecond)
		gone = exec.Command("ps", "-p", strconv.Itoa(pid)).Run() != nil
	}
	if !gone {
		t.Fatalf("orphaned launcher %d survived the reaper", pid)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("no evidence log: %v", err)
	}
	if !strings.Contains(string(log), "reaped launcher ghost pid="+strconv.Itoa(pid)) || !strings.Contains(string(log), "--- lsof -p") {
		t.Errorf("evidence log missing the ghost's record:\n%s", log)
	}
}
