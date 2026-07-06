package main

// Messages pushed into the Bubble Tea program from background client
// goroutines (via bridge.send) and handled in model.Update.
type (
	logMsg    struct{ line string }
	pointsMsg struct{ pts []point }
	statusMsg struct {
		connected, active bool
		note              string
	}
	errMsg struct{ err error }
)
