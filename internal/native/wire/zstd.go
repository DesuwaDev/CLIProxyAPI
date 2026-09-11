package wire

import (
	"bytes"
	"fmt"

	"github.com/klauspost/compress/zstd"
)

// libzstd's default (level 3) streaming output as observed from the CLI:
// no content checksum, not single-segment, window descriptor 0x58
// (2 MiB window: exponent 10+11=21, mantissa 0), frame content size omitted.
// klauspost writes a single-segment frame with FCS when the input fits the
// window, so the header is rewritten to the libzstd shape after encoding.
const observedWindowDescriptor = 0x58

var encoder = func() *zstd.Encoder {
	enc, err := zstd.NewWriter(nil,
		zstd.WithEncoderLevel(zstd.SpeedDefault),
		zstd.WithEncoderCRC(false),
		zstd.WithEncoderConcurrency(1),
		zstd.WithWindowSize(2<<20),
		zstd.WithSingleSegment(false),
		zstd.WithZeroFrames(true),
	)
	if err != nil {
		panic(fmt.Sprintf("wire: zstd encoder: %v", err))
	}
	return enc
}()

// compressBody returns the zstd frame for body with a libzstd-style header.
func compressBody(body []byte) ([]byte, error) {
	out := encoder.EncodeAll(body, make([]byte, 0, len(body)/3+64))
	return rewriteFrameHeader(out)
}

// rewriteFrameHeader drops the frame-content-size field and normalizes the
// window descriptor so the header bytes match a libzstd streaming encoder
// while the compressed blocks stay valid.
func rewriteFrameHeader(frame []byte) ([]byte, error) {
	if len(frame) < 6 || !bytes.Equal(frame[:4], []byte{0x28, 0xb5, 0x2f, 0xfd}) {
		return nil, fmt.Errorf("wire: zstd encoder produced an invalid frame")
	}
	fhd := frame[4]
	p := 5
	single := fhd&0x20 != 0
	if !single {
		p++ // window descriptor
	}
	p += int(fhd & 0x03) // dictionary id
	fcsFlag := int(fhd >> 6)
	switch {
	case fcsFlag == 0 && single:
		p++
	case fcsFlag == 1:
		p += 2
	case fcsFlag == 2:
		p += 4
	case fcsFlag == 3:
		p += 8
	}
	if p > len(frame) {
		return nil, fmt.Errorf("wire: zstd frame header truncated")
	}
	blocks := frame[p:]
	// The encoder window is capped at 2 MiB, so no block references further
	// back than that; declaring the libzstd 2 MiB window is always valid.
	out := make([]byte, 0, 6+len(blocks))
	out = append(out, 0x28, 0xb5, 0x2f, 0xfd, 0x00, observedWindowDescriptor)
	out = append(out, blocks...)
	return out, nil
}
