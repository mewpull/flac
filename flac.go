// TODO(u): Evaluate storing the samples (and residuals) during frame audio
// decoding in a buffer allocated for the stream. This buffer would be allocated
// using BlockSize and NChannels from the StreamInfo block, and it could be
// reused in between calls to Next and ParseNext. This should reduce GC
// pressure.

// Package flac provides access to FLAC (Free Lossless Audio Codec) streams.
//
// A brief introduction of the FLAC stream format [1] follows. Each FLAC stream
// starts with a 32-bit signature ("fLaC"), followed by one or more metadata
// blocks, and then one or more audio frames. The first metadata block
// (StreamInfo) describes the basic properties of the audio stream and it is the
// only mandatory metadata block. Subsequent metadata blocks may appear in an
// arbitrary order.
//
// Please refer to the documentation of the meta [2] and the frame [3] packages
// for a brief introduction of their respective formats.
//
//    [1]: https://www.xiph.org/flac/format.html#stream
//    [2]: https://godoc.org/github.com/mewkiz/flac/meta
//    [3]: https://godoc.org/github.com/mewkiz/flac/frame
package flac

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"

	"github.com/mewkiz/flac/frame"
	"github.com/mewkiz/flac/meta"
)

// A Stream contains the metadata blocks and provides access to the audio frames
// of a FLAC stream.
//
// ref: https://www.xiph.org/flac/format.html#stream
type Stream struct {
	// The StreamInfo metadata block describes the basic properties of the FLAC
	// audio stream.
	Info *meta.StreamInfo
	// Zero or more metadata blocks.
	Blocks []*meta.Block
	// Underlying io.Reader.
	r io.Reader
	// Underlying io.ReadSeeker; or nil if not present.
	rs io.ReadSeeker
	// Underlying io.Closer of file if opened with Open and ParseFile, and nil
	// otherwise.
	c io.Closer
	// Byte offset to first frame header; used for seeking.
	firstFrameHeader int64
}

// New creates a new Stream for accessing the audio samples of r. It reads and
// parses the FLAC signature and the StreamInfo metadata block, but skips all
// other metadata blocks.
//
// Call Stream.Next to parse the frame header of the next audio frame, and call
// Stream.ParseNext to parse the entire next frame including audio samples.
func New(r io.Reader) (stream *Stream, err error) {
	// Verify FLAC signature and parse the StreamInfo metadata block.
	//br := bufio.NewReader(r)
	//stream = &Stream{r: br}
	stream = &Stream{r: r}
	if rs, ok := r.(io.ReadSeeker); ok {
		stream.rs = rs
	}
	isLast, err := stream.parseStreamInfo()
	if err != nil {
		return nil, err
	}

	// Skip the remaining metadata blocks.
	for !isLast {
		//block, err := meta.New(br)
		block, err := meta.New(r)
		if err != nil && err != meta.ErrReservedType {
			return stream, err
		}
		if err = block.Skip(); err != nil {
			return stream, err
		}
		isLast = block.IsLast
	}

	// TODO: figure out how to use buffered reader, without reading "too far"
	// when storing the offset after the first frame header.

	// Store offset to first frame header.
	//if stream.rs != nil {
	//	if err := stream.storeFirstFrameOffset(); err != nil {
	//		return nil, err
	//	}
	//}
	return stream, nil
}

// flacSignature marks the beginning of a FLAC stream.
var flacSignature = []byte("fLaC")

// id3Signature marks the beginning of an ID3 stream, used to skip over ID3 data.
var id3Signature = []byte("ID3")

// parseStreamInfo verifies the signature which marks the beginning of a FLAC
// stream, and parses the StreamInfo metadata block. It returns a boolean value
// which specifies if the StreamInfo block was the last metadata block of the
// FLAC stream.
func (stream *Stream) parseStreamInfo() (isLast bool, err error) {
	// Verify FLAC signature.
	r := stream.r
	var buf [4]byte
	if _, err = io.ReadFull(r, buf[:]); err != nil {
		return false, err
	}

	// Skip prepended ID3v2 data.
	if bytes.Equal(buf[:3], id3Signature) {
		if err := stream.skipID3v2(); err != nil {
			return false, err
		}

		// Second attempt at verifying signature.
		if _, err = io.ReadFull(r, buf[:]); err != nil {
			return false, err
		}
	}

	if !bytes.Equal(buf[:], flacSignature) {
		return false, fmt.Errorf("flac.parseStreamInfo: invalid FLAC signature; expected %q, got %q", flacSignature, buf)
	}

	// Parse StreamInfo metadata block.
	block, err := meta.Parse(r)
	if err != nil {
		return false, err
	}
	si, ok := block.Body.(*meta.StreamInfo)
	if !ok {
		return false, fmt.Errorf("flac.parseStreamInfo: incorrect type of first metadata block; expected *meta.StreamInfo, got %T", si)
	}
	stream.Info = si
	return block.IsLast, nil
}

// skipID3v2 skips ID3v2 data prepended to flac files.
func (stream *Stream) skipID3v2() error {
	// Discard unnecessary data from the ID3v2 header.
	if _, err := io.CopyN(ioutil.Discard, stream.r, 2); err != nil {
		return err
	}

	// Read the size from the ID3v2 header.
	var sizeBuf [4]byte
	if _, err := io.ReadFull(stream.r, sizeBuf[:]); err != nil {
		return err
	}
	// The size is encoded as a synchsafe integer.
	size := int64(sizeBuf[0])<<21 | int64(sizeBuf[1])<<14 | int64(sizeBuf[2])<<7 | int64(sizeBuf[3])

	// Skip remaining ID3v2 header.
	if _, err := io.CopyN(ioutil.Discard, stream.r, size); err != nil {
		return err
	}
	return nil
}

// Parse creates a new Stream for accessing the metadata blocks and audio
// samples of r. It reads and parses the FLAC signature and all metadata blocks.
//
// Call Stream.Next to parse the frame header of the next audio frame, and call
// Stream.ParseNext to parse the entire next frame including audio samples.
func Parse(r io.Reader) (stream *Stream, err error) {
	// Verify FLAC signature and parse the StreamInfo metadata block.
	//br := bufio.NewReader(r)
	//stream = &Stream{r: br}
	stream = &Stream{r: r}
	if rs, ok := r.(io.ReadSeeker); ok {
		stream.rs = rs
	}
	isLast, err := stream.parseStreamInfo()
	if err != nil {
		return nil, err
	}

	// Parse the remaining metadata blocks.
	for !isLast {
		//block, err := meta.Parse(br)
		block, err := meta.Parse(r)
		if err != nil {
			if err != meta.ErrReservedType {
				return stream, err
			}
			// Skip the body of unknown (reserved) metadata blocks, as stated by
			// the specification.
			//
			// ref: https://www.xiph.org/flac/format.html#format_overview
			if err = block.Skip(); err != nil {
				return stream, err
			}
		}
		stream.Blocks = append(stream.Blocks, block)
		isLast = block.IsLast
	}

	// Store offset to first frame header.
	//if stream.rs != nil {
	//	if err := stream.storeFirstFrameOffset(); err != nil {
	//		return nil, err
	//	}
	//}
	return stream, nil
}

// Open creates a new Stream for accessing the audio samples of path. It reads
// and parses the FLAC signature and the StreamInfo metadata block, but skips
// all other metadata blocks.
//
// Call Stream.Next to parse the frame header of the next audio frame, and call
// Stream.ParseNext to parse the entire next frame including audio samples.
//
// Note: The Close method of the stream must be called when finished using it.
func Open(path string) (stream *Stream, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	stream, err = New(f)
	if err != nil {
		return nil, err
	}
	stream.c = f
	return stream, err
}

// ParseFile creates a new Stream for accessing the metadata blocks and audio
// samples of path. It reads and parses the FLAC signature and all metadata
// blocks.
//
// Call Stream.Next to parse the frame header of the next audio frame, and call
// Stream.ParseNext to parse the entire next frame including audio samples.
//
// Note: The Close method of the stream must be called when finished using it.
func ParseFile(path string) (stream *Stream, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	stream, err = Parse(f)
	if err != nil {
		return nil, err
	}
	stream.c = f
	return stream, err
}

// Close closes the stream if opened through a call to Open or ParseFile, and
// performs no operation otherwise.
func (stream *Stream) Close() error {
	if stream.c != nil {
		return stream.c.Close()
	}
	return nil
}

// Next parses the frame header of the next audio frame. It returns io.EOF to
// signal a graceful end of FLAC stream.
//
// Call Frame.Parse to parse the audio samples of its subframes.
func (stream *Stream) Next() (f *frame.Frame, err error) {
	return frame.New(stream.r)
}

// ParseNext parses the entire next frame including audio samples. It returns
// io.EOF to signal a graceful end of FLAC stream.
func (stream *Stream) ParseNext() (f *frame.Frame, err error) {
	return frame.Parse(stream.r)
}

// TODO: rename to Seek?

// SeekSample seeks to the start of the audio frame containing the specified
// sample number.
func (stream *Stream) SeekSample(sample int64) error {
	if stream.rs == nil {
		return errors.New("flac.Stream.SeekSample: reader of FLAC stream does not implement io.Seeker")
	}

	// Seek to the first sample and search for the target sample number.
	if _, err := stream.rs.Seek(stream.firstFrameHeader, io.SeekStart); err != nil {
		return err
	}
	i := int64(0)
	// TODO: optimize. Currently reads from the start of the file every time.
	for {
		// Store seeker position before parsing frame.
		framePos, err := stream.rs.Seek(0, io.SeekCurrent)
		if err != nil {
			return err
		}
		// Reset buffered reader after seek and before parsing frame.
		//stream.r = bufio.NewReader(stream.rs)
		stream.r = stream.rs
		frame, err := stream.ParseNext()
		if err != nil {
			return err
		}
		var sampleStart, sampleEnd int64
		// Calculate the start sample number of the frame.
		//
		// frame.Num specifies the frame number if the block size is fixed, and
		// the first sample number in the frame otherwise.
		if frame.HasFixedBlockSize {
			sampleStart = int64(frame.Num) * int64(frame.BlockSize)
		} else {
			sampleStart = int64(frame.Num)
		}
		sampleEnd = sampleStart + int64(frame.BlockSize)
		if sampleStart <= sample && sample < sampleEnd {
			// Reset position to the start of the frame containing the sample
			// number.
			if _, err := stream.rs.Seek(framePos, io.SeekStart); err != nil {
				return err
			}
			// Reset buffered reader after seek.
			//stream.r = bufio.NewReader(stream.rs)
			stream.r = stream.rs
			break
		}
		i += int64(frame.BlockSize)
	}
	return nil
}

// storeFirstFrameOffset stores the offset to the first frame header.
func (stream *Stream) storeFirstFrameOffset() error {
	pos, err := stream.rs.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	stream.firstFrameHeader = pos
	if _, err = stream.rs.Seek(pos, io.SeekStart); err != nil {
		return err
	}
	// Reset buffered reader after seek.
	stream.r = bufio.NewReader(stream.rs)
	return nil
}
