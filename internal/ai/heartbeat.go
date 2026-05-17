package ai

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// streamHeartbeat watches a streaming SSE goroutine for silences.
//
// Two thresholds, both optional:
//
//   - `interval` (informational): when no chunk has been seen for
//     `interval`, emit EventHeartbeat on the shared channel and reset.
//     Used as a "still alive" UI signal.
//   - `maxSilence` (load-bearing): when no chunk has been seen for
//     `maxSilence`, call `cancel` to abort the request. This catches
//     mid-stream hangs where the connection is alive but no bytes are
//     arriving. The healthy gap on a real Gemini streaming call is
//     a few seconds; anything past tens of seconds is a hang.
//
// Lifecycle:
//
//	hb := newStreamHeartbeat(ch, interval, maxSilence, cancel)
//	defer hb.stop()
//	for ... { hb.tick() ... }
//
// Time is measured against `start` via time.Since, which uses the
// monotonic clock embedded in the time.Time captured at construction.
// Wall-clock adjustments (NTP step, daylight saving, manual `date`)
// don't affect the silence measurement.
//
// Sends on `ch` are guarded by a select against stopCh so the
// watchdog cannot deadlock if the channel buffer fills and stop() is
// then called. stop must run before the surrounding goroutine closes
// the channel.
type streamHeartbeat struct {
	ch       chan<- ChatEvent
	interval time.Duration

	maxSilence   time.Duration
	cancel       context.CancelFunc
	silenceFired atomic.Bool

	// start anchors the monotonic clock reading. lastSinceStart holds
	// time.Since(start) at the moment of the most recent activity,
	// stored as nanoseconds for lockless updates.
	start          time.Time
	lastSinceStart atomic.Int64

	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// newStreamHeartbeat creates the watchdog. Pass interval=0 to disable
// EventHeartbeat emission. Pass maxSilence=0 or cancel=nil to disable
// the silence-kill behaviour. With both disabled, no goroutine is
// started — the returned object is a no-op other than tracking ticks.
func newStreamHeartbeat(ch chan<- ChatEvent, interval, maxSilence time.Duration, cancel context.CancelFunc) *streamHeartbeat {
	hb := &streamHeartbeat{
		ch:         ch,
		interval:   interval,
		maxSilence: maxSilence,
		cancel:     cancel,
		start:      time.Now(), // captures monotonic reading
		stopCh:     make(chan struct{}),
	}
	hb.lastSinceStart.Store(0)

	// Decide whether to start the watchdog goroutine and how often it
	// should tick. We tick at 1/4 of the smaller active threshold so
	// the firing latency is within 25% of the configured value.
	silenceActive := maxSilence > 0 && cancel != nil
	if interval <= 0 && !silenceActive {
		return hb
	}

	var tickBase time.Duration
	switch {
	case interval > 0 && silenceActive:
		tickBase = interval
		if maxSilence < tickBase {
			tickBase = maxSilence
		}
	case interval > 0:
		tickBase = interval
	default:
		tickBase = maxSilence
	}
	checkEvery := tickBase / 4
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

			// Silence cap: cancel the request and exit. Checked first
			// so a fired cap doesn't also emit an EventHeartbeat.
			if h.maxSilence > 0 && h.cancel != nil && elapsed >= h.maxSilence {
				h.silenceFired.Store(true)
				log.Printf("[heartbeat] silence cap fired after %v (threshold=%v)",
					elapsed.Round(time.Millisecond), h.maxSilence)
				h.cancel()
				return
			}

			// Informational heartbeat.
			if h.interval <= 0 || elapsed < h.interval {
				continue
			}
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

// silenceCapFired reports whether the watchdog called cancel because
// of a maxSilence breach. The caller can use this to distinguish
// "canceled by silence cap" from "canceled by parent context" in
// status reporting.
func (h *streamHeartbeat) silenceCapFired() bool {
	return h.silenceFired.Load()
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
