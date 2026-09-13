package distill

import "path/filepath"

func newPiReader() Reader {
	return newEnvelopeReader("pi", func() string {
		return filepath.Join(homeDir(), ".pi", "agent", "sessions")
	})
}
