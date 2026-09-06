package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"time"
)

// setupLogging sets up logging to stdout and a file.
func setupLogging() (func(), error) {
	if err := os.Rename("log.txt", "old_log.txt"); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("rotate log file: %w", err)
	}

	logFile, err := os.OpenFile("log.txt", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}

	log.SetOutput(io.MultiWriter(os.Stdout, logFile))
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)

	return func() { logFile.Close() }, nil
}

// runWithTimeout runs closeFn in a goroutine and waits up to timeout for
// it to finish. If it doesn't finish in time, runWithTimeout returns
// anyway and logs a warning. This aims to ensure a goroutine cannot
// block shutdown. Intended for use with defer.
func runWithTimeout(name string, closeFn func(), timeout time.Duration) {
	done := make(chan struct{})
	go func() {
		closeFn()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		log.Printf("%s: close did not finish within %v, abandoning", name, timeout)
	}
}
