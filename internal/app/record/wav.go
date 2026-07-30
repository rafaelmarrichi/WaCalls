package record

import (
	"encoding/binary"
	"io"
	"os"
)

// Canonical WAV output for call recording: PCM signed 16-bit little endian,
// two interleaved channels at 16 kHz. Left channel is what we sent to the
// contact (agent microphone plus any injected announcement), right channel is
// what the contact sent us.
//
// The two size fields cannot be known when the file is created, so the header
// goes out with zeros and is patched on close. A recording cut short by a crash
// therefore keeps a zeroed header: still decodable by tolerant tools, and
// detectable by us because the sizes do not match the file length on disk.

const (
	sampleRate    = 16000
	channels      = 2
	bitsPerSample = 16
	bytesPerFrame = channels * bitsPerSample / 8

	headerSize = 44

	// Offsets of the two size fields patched on close.
	offsetRiffSize = 4
	offsetDataSize = 40
)

// writeHeader emits a 44-byte canonical header with both size fields zeroed.
func writeHeader(w io.Writer) error {
	h := make([]byte, 0, headerSize)

	h = append(h, "RIFF"...)
	h = binary.LittleEndian.AppendUint32(h, 0) // patched on close
	h = append(h, "WAVE"...)

	h = append(h, "fmt "...)
	h = binary.LittleEndian.AppendUint32(h, 16) // PCM fmt chunk length
	h = binary.LittleEndian.AppendUint16(h, 1)  // format 1 = PCM
	h = binary.LittleEndian.AppendUint16(h, channels)
	h = binary.LittleEndian.AppendUint32(h, sampleRate)
	h = binary.LittleEndian.AppendUint32(h, sampleRate*bytesPerFrame) // byte rate
	h = binary.LittleEndian.AppendUint16(h, bytesPerFrame)            // block align
	h = binary.LittleEndian.AppendUint16(h, bitsPerSample)

	h = append(h, "data"...)
	h = binary.LittleEndian.AppendUint32(h, 0) // patched on close

	_, err := w.Write(h)
	return err
}

// patchSizes rewrites the two length fields with the real totals. Called after
// the buffered writer has been flushed, so dataBytes matches what is on disk.
func patchSizes(f *os.File, dataBytes uint32) error {
	var buf [4]byte

	binary.LittleEndian.PutUint32(buf[:], headerSize-8+dataBytes)
	if _, err := f.WriteAt(buf[:], offsetRiffSize); err != nil {
		return err
	}

	binary.LittleEndian.PutUint32(buf[:], dataBytes)
	_, err := f.WriteAt(buf[:], offsetDataSize)
	return err
}

// interleave writes one frame as left/right pairs of signed 16-bit samples.
// dst must hold len(left)*bytesPerFrame bytes and left/right must be equal length.
//
// Scaling uses 32767 rather than 32768 so a sample at exactly +1.0 does not wrap
// to the most negative value, which would land as a click in the recording.
func interleave(dst []byte, left, right []float32) {
	for i := range left {
		binary.LittleEndian.PutUint16(dst[i*4:], uint16(toPCM16(left[i])))
		binary.LittleEndian.PutUint16(dst[i*4+2:], uint16(toPCM16(right[i])))
	}
}

func toPCM16(sample float32) int16 {
	switch {
	case sample >= 1:
		return 32767
	case sample <= -1:
		return -32767
	default:
		return int16(sample * 32767)
	}
}
