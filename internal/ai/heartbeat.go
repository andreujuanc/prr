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
// Time is measured against `start` via time.Since, which uses the
// monotonic clock embedded in the time.Time captured at construction.
// Wall-clock adjustments (NTP step, daylight saving, manual `date`)
// don't affect the silence measurement.
//
// Sends are guarded by a select against stopCh so the watchdog cannot
// deadlock if the channel buffer fills and stop() is then called.
// stop must run before the surrounding goroutine closes the channel.
type streamHeartbeat struct {
	ch       chan<- ChatEvent
	interval time.Duration

	// start anchors the monotonic clock reading. lastSinceStart holds
	// time.Since(start) at the moment of the most recent activity,
	// stored as nanoseconds for lockless updates.
	start            time.Time
	lastSinceStart   atomic.Int64

	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

func newStreamHeartbeat(ch chan<- ChatEvent, interval time.Duration) *streamHeartbeat {
	hb := &streamHeartbeat{
		ch:       ch,
		interval: interval,
		start:    time.Now(), // captures monotonic reading
		stopCh:   make(chan struct{}),
	}
	hb.lastSinceStart.Store(0)

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
			now := time.Since(h.start)
			last := time.Duration(h.lastSinceStart.Load())
			elapsed := now - last
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
			h.lastSinceStart.Store(int64(time.Since(h.start)))
		case <-h.stopCh:
			return
		}
	}
}

// tick records activity. Cheap; safe to call on every chunk.
func (h *streamHeartbeat) tick() {
	h.lastSinceStart.Store(int64(time.Since(h.start)))
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
