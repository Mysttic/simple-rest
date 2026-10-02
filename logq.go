// logq.go: console lines that never hold up the code that writes them.
//
// A console can stop taking output: QuickEdit in a Windows console pauses the
// program's writes while text is selected, and a terminal does the same after
// XOFF (Ctrl+S). log.Logger serializes writes with a mutex, so one stuck write
// would stop every handler that logs after it. Handlers, the guard and the
// background work therefore log through a short queue that one goroutine
// writes out. When the queue is full the line is dropped and counted, and the
// count comes out just before the next line that gets in. The updater keeps
// log.Printf: scripts/e2e-update.sh looks for its last line right before the
// process exits.
package main

import (
	"fmt"
	"sync/atomic"
	"time"
)

const logQueueSize = 64

type logEntry struct {
	line    string
	dropped int64         // lines dropped right before this one
	done    chan struct{} // set on the marker flush waits for; such an entry has no line
}

type logQueue struct {
	ch      chan logEntry
	dropped atomic.Int64
	write   func(string)
}

// newLogQueue starts the goroutine that hands queued lines to write.
func newLogQueue(size int, write func(string)) *logQueue {
	q := &logQueue{ch: make(chan logEntry, size), write: write}
	go q.run()
	return q
}

func (q *logQueue) run() {
	for e := range q.ch {
		if e.dropped > 0 {
			q.write(fmt.Sprintf("(%d log lines dropped)", e.dropped))
		}
		if e.done != nil {
			close(e.done)
			continue
		}
		q.write(e.line)
	}
}

// printf queues one line and returns at once, also when the console is stuck.
func (q *logQueue) printf(format string, args ...any) {
	e := logEntry{line: fmt.Sprintf(format, args...), dropped: q.dropped.Swap(0)}
	select {
	case q.ch <- e:
	default:
		q.dropped.Add(e.dropped + 1)
	}
}

// flush waits until the lines queued before it are written, together with the
// count of dropped ones, but no longer than timeout. It reports whether it got
// there in time.
func (q *logQueue) flush(timeout time.Duration) bool {
	t := time.NewTimer(timeout)
	defer t.Stop()
	e := logEntry{done: make(chan struct{}), dropped: q.dropped.Swap(0)}
	select {
	case q.ch <- e:
	case <-t.C:
		q.dropped.Add(e.dropped)
		return false
	}
	select {
	case <-e.done:
		return true
	case <-t.C:
		return false
	}
}
