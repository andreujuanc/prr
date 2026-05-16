package ai

import (
	"sync"
	"sync/atomic"
	"time"
)

// streamHeartbeat watches a streaming SSE goroutine for silences. When
// no chunk has been seen for `interval` it emits EventHeartbeat on the
// shared channel, then resets so it won't spam.
//
// Lifecycle:
//
//	hb := newStreamHeartbeat(ch, interval)
//	defer hb.stop()
//	for ... { hb.tick() ... }
//
// Sends are guarded by a select against stopCh so the watchdog cannot
// deadlock if the channel buffer fills and stop() is then called.
// stop must run before the surrounding goroutine closes the channel.
type streamHeartbeat struct {
	ch       chan<- ChatEvent
	interval time.Duration
	lastNano atomic.Int64

	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

func newStreamHeartbeat(ch chan<- ChatEvent, interval time.Duration) *streamHeartbeat {
	hb := &streamHeartbeat{
		ch:       ch,
		interval: interval,
		stopCh:   make(chan struct{}),
	}
	hb.lastNano.Store(time.Now().UnixNano())

	if interval <= 0 {
		return hb
	}

	// Check four times per interval — quick enough to fire near the
	// configured silence threshold without being noisy.
	checkEvery := interval / 4
	if checkEvery < 100*time.Millisecond {
		checkEvery = 100 * time.Millisecond
	}

	hb.wg.Add(1)
	go hb.watch(checkEvery)
	return hb
}

func (h *streamHeartbeat) watch(checkEvery time.Duration) {
	defer h.wg.Done()
	ticker := time.NewTicker(checkEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			elapsed := time.Duration(time.Now().UnixNano() - h.lastNano.Load())
			if elapsed < h.interval {
				continue
			}
			// Use a non-blocking-on-stop send so a full channel +
			// later stop() can't deadlock wg.Wait.
			select {
			case h.ch <- ChatEvent{Type: EventHeartbeat, Silence: elapsed}:
			case <-h.stopCh:
				return
			}
			h.lastNano.Store(time.Now().UnixNano())
		case <-h.stopCh:
			return
		}
	}
}

// tick records activity. Cheap; safe to call on every chunk.
func (h *streamHeartbeat) tick() {
	h.lastNano.Store(time.Now().UnixNano())
}

// stop terminates the watchdog goroutine. Idempotent. Must be called
// before the surrounding goroutine closes the channel — close(ch)
// after the watchdog has fully exited.
func (h *streamHeartbeat) stop() {
	h.stopOnce.Do(func() {
		close(h.stopCh)
		h.wg.Wait()
	})
}
