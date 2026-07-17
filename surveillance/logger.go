package main

import (
	"fmt"
	"os"
	"time"
)

// This subservice is a standalone binary that runs as its own OS process, so it
// cannot import the ArozOS managed logger (a different Go module). To stay
// compliant with the "no standard log package" convention while still producing
// readable, timestamped output on stderr, all logging goes through these tiny
// helpers built on fmt. ArozOS captures the child process' stderr in its
// subservice logs.

// logInfo prints an informational line to stderr.
func logInfo(title, message string) {
	fmt.Fprintf(os.Stderr, "%s [INFO]  %s: %s\n", time.Now().Format(time.RFC3339), title, message)
}

// logError prints an error line to stderr. err may be nil.
func logError(title, message string, err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s [ERROR] %s: %s (%v)\n", time.Now().Format(time.RFC3339), title, message, err)
		return
	}
	fmt.Fprintf(os.Stderr, "%s [ERROR] %s: %s\n", time.Now().Format(time.RFC3339), title, message)
}
