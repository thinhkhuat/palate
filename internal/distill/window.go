package distill

// Window splits all (oldest-first, full transcript) into previouslyAnalyzed
// and new. new = messages with Index > cursorIndex (pass -1 for "no cursor
// yet" — every message is new). previouslyAnalyzed is up to
// max(20, len(new)) messages immediately before the first new message,
// clamped to the start of the transcript — mirroring Command Code's own
// Wy=20 windowing cadence. If new is empty, the caller skips the gateway
// call entirely: re-running on an unchanged transcript is then free.
func Window(all []Message, cursorIndex int) (previouslyAnalyzed, newMsgs []Message) {
	splitAt := len(all)
	for i, m := range all {
		if m.Index > cursorIndex {
			splitAt = i
			break
		}
	}
	newMsgs = all[splitAt:]

	windowSize := len(newMsgs)
	if windowSize < 20 {
		windowSize = 20
	}
	start := splitAt - windowSize
	if start < 0 {
		start = 0
	}
	previouslyAnalyzed = all[start:splitAt]
	return previouslyAnalyzed, newMsgs
}
