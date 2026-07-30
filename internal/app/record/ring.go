package record

// ring is a fixed-capacity FIFO of PCM samples with one job: never block the
// media path. Audio arrives on the RTP receive path and on the browser data
// channel, both of which must not wait on disk. When the buffer is full the
// oldest samples are discarded and counted, because dropping old audio degrades
// the recording while blocking would degrade the live call.
//
// Not safe for concurrent use; the Recorder holds its mutex around every call.
type ring struct {
	buf     []float32
	head    int // index of the oldest sample
	count   int // samples currently held
	dropped int64
}

func newRing(capacity int) *ring {
	return &ring{buf: make([]float32, capacity)}
}

// write appends pcm, discarding the oldest samples when capacity is reached.
func (r *ring) write(pcm []float32) {
	for _, s := range pcm {
		if r.count == len(r.buf) {
			r.head = (r.head + 1) % len(r.buf)
			r.count--
			r.dropped++
		}
		r.buf[(r.head+r.count)%len(r.buf)] = s
		r.count++
	}
}

// read fills out from the front, padding the remainder with silence, and returns
// how many real samples it produced. The padding is what keeps the recording
// aligned with wall-clock time when a side is not sending: the whole point of
// the fixed clock is that a silent side occupies its own duration in the file
// instead of shifting the other side backwards.
func (r *ring) read(out []float32) int {
	n := 0
	for n < len(out) && r.count > 0 {
		out[n] = r.buf[r.head]
		r.head = (r.head + 1) % len(r.buf)
		r.count--
		n++
	}
	for i := n; i < len(out); i++ {
		out[i] = 0
	}
	return n
}
