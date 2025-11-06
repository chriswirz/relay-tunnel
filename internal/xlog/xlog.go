// Package xlog is a tiny leveled logger so the rest of the code stays terse.
package xlog

import (
	"fmt"
	"os"
	"sync/atomic"
	"time"
)

var verbose atomic.Bool

// SetVerbose toggles debug output.
func SetVerbose(v bool) { verbose.Store(v) }

func ts() string { return time.Now().Format("15:04:05.000") }

// Debugf logs only when verbose is enabled.
func Debugf(format string, a ...any) {
	if verbose.Load() {
		fmt.Fprintf(os.Stderr, "%s DBG %s\n", ts(), fmt.Sprintf(format, a...))
	}
}

// Infof logs an informational line to stderr.
func Infof(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "%s INF %s\n", ts(), fmt.Sprintf(format, a...))
}

// Errorf logs an error line to stderr.
func Errorf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "%s ERR %s\n", ts(), fmt.Sprintf(format, a...))
}

// Fatalf logs and exits non-zero.
func Fatalf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "%s FATAL %s\n", ts(), fmt.Sprintf(format, a...))
	os.Exit(1)
}
