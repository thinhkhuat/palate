package distill

import (
	"os"
	"os/exec"
)

// SpawnLearnSession starts `bin learn session <transcriptPath>` as a
// detached child process and returns as soon as the OS has accepted the
// Start() call — it never Waits. Callers are hook handlers that must
// return in well under a second regardless of how long the distiller's
// LLM call takes, so the parent returns before the child's HTTP request
// even begins.
// learnSessionCmd builds the detached `learn session` command. Split out so a
// test can assert what the child inherits without starting a process.
func learnSessionCmd(bin, transcriptPath string) *exec.Cmd {
	cmd := exec.Command(bin, "learn", "session", transcriptPath)
	// nil Stdin/Stdout/Stderr: Cmd discards them when not connected to a
	// pipe, equivalent to redirecting to /dev/null without needing one.
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	if wd, err := os.Getwd(); err == nil {
		// Explicit, not relied-on-inherited: makes the child's working
		// directory (and therefore its legacy-layout detection) independent
		// of platform/launch-mechanism quirks.
		cmd.Dir = wd
	}
	// Explicit for the same reason: the child must resolve the same Palate
	// home as the hook that spawned it (PALATE_HOME / HOME), and a test pins
	// that the full environment is handed over.
	cmd.Env = os.Environ()
	cmd.SysProcAttr = detachAttrs()
	return cmd
}

func SpawnLearnSession(bin, transcriptPath string) error {
	return learnSessionCmd(bin, transcriptPath).Start()
}
