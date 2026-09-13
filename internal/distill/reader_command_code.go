package distill

import "path/filepath"

func newCommandCodeReader() Reader {
	return newEnvelopeReader("command_code", func() string {
		return filepath.Join(homeDir(), ".commandcode", "projects")
	})
}
