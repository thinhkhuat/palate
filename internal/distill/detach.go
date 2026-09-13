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
func SpawnLearnSession(bin, transcriptPath string) error {
	cmd := exec.Command(bin, "learn", "session", transcriptPath)
	// nil Stdin/Stdout/Stderr: Cmd discards them when not connected to a
	// pipe, equivalent to redirecting to /dev/null without needing one.
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	if wd, err := os.Getwd(); err == nil {
		// Explicit, not relied-on-inherited: makes the child's working
		// directory (and therefore its relative config.yaml/db_path
		// resolution) independent of platform/launch-mechanism quirks.
		cmd.Dir = wd
	}
	cmd.SysProcAttr = detachAttrs()
	return cmd.Start()
}
