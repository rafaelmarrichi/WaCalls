package player

import (
	"encoding/binary"
	"fmt"
)

// Announcement assets are mono 16 kHz PCM, which is what the call audio path
// expects. Anything else is rejected here rather than resampled: a wrong sample
// rate would play at the wrong speed and pitch, and a caller hearing a chipmunk
// read the legal notice is worse than a clear failure.
//
// The parser walks the chunk list instead of assuming a 44-byte header, because
// files exported by ordinary audio tools carry LIST or fact chunks before the
// data and would otherwise decode as noise.

const (
	wantSampleRate = 16000
	wantChannels   = 1
	wantBits       = 16

	formatPCM = 1
)

// decodeWAV returns the samples of a mono 16 kHz 16-bit PCM WAV file.
func decodeWAV(raw []byte) ([]float32, error) {
	if len(raw) < 12 {
		return nil, fmt.Errorf("file is too short to be a WAV")
	}
	if string(raw[0:4]) != "RIFF" || string(raw[8:12]) != "WAVE" {
		return nil, fmt.Errorf("not a RIFF/WAVE file")
	}

	var (
		data      []byte
		haveFmt   bool
		haveData  bool
		numChans  uint16
		rate      uint32
		bits      uint16
		audioForm uint16
	)

	for pos := 12; pos+8 <= len(raw); {
		id := string(raw[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(raw[pos+4 : pos+8]))
		body := pos + 8

		if size < 0 || body+size > len(raw) {
			// A truncated final chunk: take what is there for data, ignore the rest.
			size = len(raw) - body
		}

		switch id {
		case "fmt ":
			if size < 16 {
				return nil, fmt.Errorf("fmt chunk is %d bytes, expected at least 16", size)
			}
			audioForm = binary.LittleEndian.Uint16(raw[body:])
			numChans = binary.LittleEndian.Uint16(raw[body+2:])
			rate = binary.LittleEndian.Uint32(raw[body+4:])
			bits = binary.LittleEndian.Uint16(raw[body+14:])
			haveFmt = true
		case "data":
			data = raw[body : body+size]
			haveData = true
		}

		// Chunks are word aligned: an odd size is followed by a pad byte.
		pos = body + size
		if size%2 == 1 {
			pos++
		}
	}

	if !haveFmt {
		return nil, fmt.Errorf("no fmt chunk")
	}
	if !haveData {
		return nil, fmt.Errorf("no data chunk")
	}
	if audioForm != formatPCM {
		return nil, fmt.Errorf("audio format %d is not uncompressed PCM", audioForm)
	}
	if numChans != wantChannels {
		return nil, fmt.Errorf("%d channels, expected mono", numChans)
	}
	if rate != wantSampleRate {
		return nil, fmt.Errorf("%d Hz, expected %d", rate, wantSampleRate)
	}
	if bits != wantBits {
		return nil, fmt.Errorf("%d bits per sample, expected %d", bits, wantBits)
	}

	n := len(data) / 2
	if n == 0 {
		return nil, fmt.Errorf("data chunk is empty")
	}

	pcm := make([]float32, n)
	for i := range n {
		pcm[i] = float32(int16(binary.LittleEndian.Uint16(data[i*2:]))) / 32768
	}
	return pcm, nil
}

// durationMs is how long these samples take to play at the call sample rate.
func durationMs(samples int) int64 {
	return int64(samples) * 1000 / wantSampleRate
}
