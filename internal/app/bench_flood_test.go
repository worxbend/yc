package app

import (
	"fmt"
	"testing"
)

// The benchmarks below replay the same deterministic flood the stress tests
// use (stressBurst) through the real Update/View pipeline, so a perf change
// is measured against exactly the load the stress tests already prove
// correct. They share the fake clock, so no result depends on wall time.

// benchModel builds the same live shell the stress tests use, preloaded with
// a full scrollback of history so View renders a saturated chat pane.
func benchModel(b *testing.B, preload int) (shellModel, *stressClock) {
	b.Helper()
	clock := &stressClock{now: stressStart}
	// "off" keeps the reveal queue out of the picture: these benchmarks
	// measure ingestion and rendering, and the animation package has its own
	// tests. The flood benchmarks that want animation set it explicitly.
	model, _ := newStressModel(b, clock, "off", 100, 30)
	for _, message := range stressBurst(preload) {
		model = feedStress(b, model, message)
	}
	return model, clock
}

// BenchmarkChatFloodUpdate measures ingestion: one normalized message driven
// through Update, including dedupe, scrollback trimming, and roster updates.
// IDs are unique per delivery so the dedupe ring never short-circuits the
// append path; ns/op here is the per-message ingestion cost.
func BenchmarkChatFloodUpdate(b *testing.B) {
	model, _ := benchModel(b, stressScrollback)
	burst := stressBurst(1024)
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		message := burst[i%len(burst)]
		message.ID = fmt.Sprintf("bench-%d", i)
		next, _ := model.Update(chatClientMessageMsg{message: message, ok: true})
		model = next.(shellModel)
		i++
	}
}

// BenchmarkChatViewWarmCache measures a repaint of an unchanged frame: every
// row is served from the shared row cache. This is the steady-state cost of
// a frame between messages and should stay far below the cold path.
func BenchmarkChatViewWarmCache(b *testing.B) {
	model, _ := benchModel(b, stressScrollback)
	_ = model.View() // warm the cache once
	b.ReportAllocs()
	for b.Loop() {
		_ = model.View()
	}
}

// BenchmarkChatViewColdCache measures a full re-render of the visible frame
// with the row cache emptied every iteration, as happens on a resize or theme
// change. The gap between this and the warm benchmark is what the cache buys.
func BenchmarkChatViewColdCache(b *testing.B) {
	model, _ := benchModel(b, stressScrollback)
	b.ReportAllocs()
	for b.Loop() {
		sharedRowCache = newChatRowCache()
		_ = model.View()
	}
	b.StopTimer()
	sharedRowCache = newChatRowCache()
}

// BenchmarkChatPipelineUpdateAndView measures the full per-message cost the
// terminal actually pays under flood: deliver one message, repaint the frame.
// msgs/s sustained is 1e9 / ns/op for this benchmark.
func BenchmarkChatPipelineUpdateAndView(b *testing.B) {
	model, _ := benchModel(b, stressScrollback)
	burst := stressBurst(1024)
	_ = model.View()
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		message := burst[i%len(burst)]
		message.ID = fmt.Sprintf("bench-%d", i)
		next, _ := model.Update(chatClientMessageMsg{message: message, ok: true})
		model = next.(shellModel)
		_ = model.View()
		i++
	}
}
