package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

// installGoroutineDumpHandler registers a SIGUSR1 handler that writes
// every goroutine's stack trace to <cacheDir>/prr/goroutines-<unix>.txt
// and keeps the process running.
//
// Use this when a phase appears to hang: `kill -USR1 <pid>`. Unlike
// SIGQUIT (which prints to stderr and exits), the dump survives in a
// file that's readable after the fact. Safe to invoke any number of
// times — each call writes a fresh timestamped file.
func installGoroutineDumpHandler() {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		cacheDir = os.TempDir()
	}
	dir := filepath.Join(cacheDir, "prr")

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGUSR1)
	go func() {
		for range ch {
			if err := dumpGoroutines(dir); err != nil {
				log.Printf("goroutine dump failed: %v", err)
			}
		}
	}()
}

func dumpGoroutines(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// Grow the buffer until runtime.Stack returns less than the buffer
	// length — meaning every goroutine fit. Start at 256KB; cap at 64MB
	// so a runaway process can't OOM the host.
	const cap = 64 * 1024 * 1024
	buf := make([]byte, 256*1024)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		if len(buf) >= cap {
			buf = buf[:n]
			break
		}
		buf = make([]byte, len(buf)*2)
	}

	path := filepath.Join(dir, fmt.Sprintf("goroutines-%d.txt", time.Now().Unix()))
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		return err
	}
	log.Printf("goroutine dump written: %s (%d bytes, %d goroutines)", path, len(buf), runtime.NumGoroutine())
	return nil
}
