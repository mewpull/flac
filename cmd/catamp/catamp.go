// It really whips the cat's ass.
//
//
// $ catamp *.flac
//
// Remote pausing is done with SIGTSTP
// $ ps -C catamp | tail -n 1 | awk '{print $1}' | xargs -I '{}' kill -s SIGTSTP '{}'
//
// Remote playing is done with SIGCONT
// $ ps -C catamp | tail -n 1 | awk '{print $1}' | xargs -I '{}' kill -s SIGCONT '{}'
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Sirupsen/logrus"
	"github.com/mesilliac/pulse-simple"
	"github.com/mewkiz/flac"
	"github.com/mewkiz/flac/frame"
)

var (
	floatFlag bool
)

func init() {
	flag.BoolVar(&floatFlag, "float", false, "use float values for samples")
}

func main() {
	flag.Parse()
	if flag.NArg() < 1 {
		log.Fatalln("gief flac")
	}

	// disable input buffering
	exec.Command("stty", "-F", "/dev/tty", "cbreak", "min", "1").Run()
	// do not display entered characters on the screen
	exec.Command("stty", "-F", "/dev/tty", "-echo").Run()
	// restore the echoing state when exiting
	defer exec.Command("stty", "-F", "/dev/tty", "echo").Run()

	for _, f := range flag.Args() {
		if err := play(f); err != nil {
			log.Fatalln(err)
		}
	}
}

func play(filename string) (err error) {
	stream, err := flac.ParseFile(filename)
	if err != nil {
		fmt.Println(filename)
		return err
	}
	defer stream.Close()

	// Number of bytes for a sample. Used by semi.
	var size int

	var sf pulse.SampleFormat
	if floatFlag {
		sf = pulse.SAMPLE_FLOAT32LE
		if stream.Info.BitsPerSample == 24 {
			logrus.Warnln("not implemented - sounds terrible")
		}
	} else {
		if stream.Info.BitsPerSample == 16 {
			sf = pulse.SAMPLE_S24_32LE
			size = 4
		} else if stream.Info.BitsPerSample == 24 {
			logrus.Warnln("not implemented - only one channel used")
			sf = pulse.SAMPLE_S24_32BE
			size = 8
		}
	}

	ss := pulse.SampleSpec{sf, stream.Info.SampleRate, stream.Info.NChannels}
	pb, err := initPulse(&ss)
	if err != nil {
		return err
	}
	defer pb.Free()
	defer pb.Drain()
	pb.Flush()
	pb.Drain()

	go func() {
		var b = []byte{0x00}
		quit := make(chan bool)
		go func(quit chan bool) {
			fmt.Print("                                                                      ")
			for {
				cat := []string{
					"\r(^-.-^)ﾉ - Now playing: %s",
					"\r(^._.^)ﾉ - Now playing: %s",
					"\r(^+_+^)ﾉ - Now playing: %s",
				}
				for i := 0; i < len(filename); i++ {
					select {
					case <-quit:
						return
					default:
						fmt.Printf(cat[i%len(cat)], strings.Repeat(filename+" ", 2)[i:i+len(filename)])
						time.Sleep(200 * time.Millisecond)
					}
				}
			}
		}(quit)
		for {
			os.Stdin.Read(b)
			if b[0] == 'n' {
				stream.Seek(0, io.SeekEnd)
				quit <- true
				return
			}
		}
	}()
	if floatFlag {
		floatSolution(pb, stream)
	} else {
		intSolution(pb, stream, size)
	}
	return nil
}

func initPulse(ss *pulse.SampleSpec) (*pulse.Stream, error) {
	return pulse.Playback("pulse-simple test", "playback test", ss)
}

// intSolution is the best at the moment. Plays music perfectly, with no audio
// clicks. On two channels
//
// However, 24 bits per sample flac files are unable to play both channels at
// the same time. Since I haven't been able to find a suitable SampleFormat.
func intSolution(pb *pulse.Stream, stream *flac.Stream, size int) {
	data := []byte{}
	tmp := make([]byte, size)

	for {
		frame, err := stream.ParseNext()
		if err != nil {
			if err == io.EOF {
				break
			} else {
				log.Fatalln(err)
			}
		}
		if stream.Info.BitsPerSample == 24 {
			for j, _ := range frame.Subframes[0].Samples {
				binary.BigEndian.PutUint32(tmp, uint32(frame.Subframes[0].Samples[j]))
				data = append(data, tmp...)
			}
		} else {
			for j, _ := range frame.Subframes[0].Samples {
				binary.BigEndian.PutUint32(tmp, uint32(frame.Subframes[0].Samples[j]))
				data = append(data, tmp...)
				binary.BigEndian.PutUint32(tmp, uint32(frame.Subframes[1].Samples[j]))
				data = append(data, tmp...)
			}
		}
		if len(data) >= int(stream.Info.SampleRate)*size {
			s := int(stream.Info.SampleRate) * size
			if len(data) < s {
				s = len(data)
			}
			pb.Write(data[:s])
			data = data[s:]
		}
	}
}

// Contains unread samples for next iteration.
var samples = []int32{}

func readSamples(stream *flac.Stream, n int) (data []byte, err error) {
	var frame *frame.Frame
	for len(samples) < n && err == nil {
		frame, err = stream.ParseNext()
		if err != nil {
			break
		}
		for i := range frame.Subframes[0].Samples {
			samples = append(samples, frame.Subframes[0].Samples[i])
			samples = append(samples, frame.Subframes[1].Samples[i])
		}
	}
	// If we reached EOF, we didn't fill our samples, but we still want 'em
	// all!
	if n > len(samples) {
		n = len(samples)
	}

	// Convert to floats (magic!).
	ret := make([]float32, n)
	for i, s := range samples[:n] {
		ret[i] = (float32(s) - math.MinInt16) / (math.MaxInt16 - math.MinInt16)
	}
	samples = samples[n:]

	buf := new(bytes.Buffer)
	for _, s := range ret {
		_ = binary.Write(buf, binary.LittleEndian, s)
	}
	return buf.Bytes(), err
}

// Plays music perfectly if it's 16 bit per samples.
//
// No other flac files are supported.
//
// When starting, stopping the playback there are clicks and pops. Also when
// increasing and decreasing the volume. Might be some problems with the pulse
// audio library.
func floatSolution(pb *pulse.Stream, stream *flac.Stream) {
	for {
		buf, err := readSamples(stream, int(stream.Info.SampleRate)*1)
		if err != nil {
			if io.EOF == err {
				pb.Write(buf)
				break
			}
			log.Fatalln(err)
		}
		pb.Write(buf)
	}
}
