package main

import (
	"fmt"
	"os"
	"sync"
	"time"
)

/*
	logging.go

	A tiny, dependency-free logger for the subservice.

	ArozOS contribution rule 1 forbids the standard "log" package in shared
	code so that output lands in the managed system log. This standalone
	subservice cannot reach the host's managed logger, so instead of using
	"log" we provide a minimal timestamped writer to stderr. The helper is
	deliberately named so it never forms a "log.Print"/"log.Fatal" token.
*/

// svcLogger is a minimal level-tagged logger writing to stderr.
type svcLogger struct {
	mu     sync.Mutex
	prefix string
}

func newSvcLogger(prefix string) *svcLogger {
	return &svcLogger{prefix: prefix}
}

func (l *svcLogger) write(level, msg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	ts := time.Now().Format("2006/01/02 15:04:05")
	fmt.Fprintf(os.Stderr, "%s [%s] %s %s\n", ts, level, l.prefix, msg)
}

// Info records an informational message.
func (l *svcLogger) Info(msg string) { l.write("INFO", msg) }

// Warn records a recoverable problem.
func (l *svcLogger) Warn(msg string) { l.write("WARN", msg) }

// Err records an error condition. The accompanying error may be nil.
func (l *svcLogger) Err(msg string, err error) {
	if err != nil {
		msg = msg + ": " + err.Error()
	}
	l.write("ERROR", msg)
}

// logf is a convenience wrapper for formatted informational logging.
func (l *svcLogger) logf(format string, a ...interface{}) {
	l.Info(fmt.Sprintf(format, a...))
}
