package calling

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

// mediaRuntime is a process-resident media cache. IVR assets are parsed once
// into Opus packets and then reused by every call. This removes file I/O and
// OGG parsing from the realtime call path and keeps the media engine hot for
// the lifetime of the server process.
type mediaRuntime struct {
	mu    sync.RWMutex
	cache map[string][][]byte
}

var hotMedia = newMediaRuntime()

func newMediaRuntime() *mediaRuntime {
	r := &mediaRuntime{cache: make(map[string][][]byte)}
	// Prewarm the conventional audio directory without blocking startup.
	// Assets outside this directory are cached lazily on first use.
	go r.prewarmDir("./audio")
	return r
}

func (r *mediaRuntime) prewarmDir(dir string) {
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() {
			return nil
		}
		if strings.EqualFold(filepath.Ext(path), ".ogg") || strings.EqualFold(filepath.Ext(path), ".opus") {
			_, _ = r.packets(path)
		}
		return nil
	})
}

func (r *mediaRuntime) packets(filePath string) ([][]byte, error) {
	clean := filepath.Clean(filePath)
	r.mu.RLock()
	if packets, ok := r.cache[clean]; ok {
		r.mu.RUnlock()
		return packets, nil
	}
	r.mu.RUnlock()

	f, err := os.Open(clean)
	if err != nil {
		return nil, fmt.Errorf("failed to open audio file: %w", err)
	}
	packets, err := readOpusPackets(f)
	_ = f.Close()
	if err != nil {
		return nil, fmt.Errorf("failed to read Opus packets: %w", err)
	}
	if len(packets) == 0 {
		return nil, fmt.Errorf("audio file contains no playable Opus packets")
	}

	r.mu.Lock()
	// Preserve the first successfully parsed copy if multiple calls raced on
	// the same cold asset.
	if existing, ok := r.cache[clean]; ok {
		packets = existing
	} else {
		r.cache[clean] = packets
	}
	r.mu.Unlock()
	return packets, nil
}

// AudioPlayer is a realtime RTP sender backed by the process-resident media
// cache. Sequence/timestamp state is preserved across prompts so Meta never
// sees each IVR node as a new RTP stream.
type AudioPlayer struct {
	track          *webrtc.TrackLocalStaticRTP
	stop           chan struct{}
	sequenceNumber uint16
	timestamp      uint32
	mu             sync.Mutex
}

func NewAudioPlayer(track *webrtc.TrackLocalStaticRTP) *AudioPlayer {
	return &AudioPlayer{
		track: track,
		stop:  make(chan struct{}),
	}
}

func (p *AudioPlayer) SetSequence(seq uint16, ts uint32) {
	p.mu.Lock()
	p.sequenceNumber = seq + 1
	p.timestamp = ts + 960
	p.mu.Unlock()
}

func (p *AudioPlayer) Sequence() (uint16, uint32) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sequenceNumber, p.timestamp
}

// writeOpus writes one 20 ms Opus RTP frame while atomically advancing the
// RTP clock. TrackLocalStaticRTP owns SSRC rewriting for the negotiated sender;
// keeping one sequence/timestamp timeline is what matters to the receiver.
func (p *AudioPlayer) writeOpus(opusData []byte) error {
	if p.track == nil {
		return fmt.Errorf("audio track is not available")
	}

	p.mu.Lock()
	pkt := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    111,
			SequenceNumber: p.sequenceNumber,
			Timestamp:      p.timestamp,
		},
		Payload: opusData,
	}
	p.sequenceNumber++
	p.timestamp += 960
	p.mu.Unlock()

	return p.track.WriteRTP(pkt)
}

// PlayFile plays a cached OGG/Opus asset with a monotonic 20 ms media clock.
// The first frame is emitted immediately instead of waiting for the first
// ticker edge; this materially reduces answer-to-greeting latency.
func (p *AudioPlayer) PlayFile(filePath string) (int, error) {
	packets, err := hotMedia.packets(filePath)
	if err != nil {
		return 0, err
	}

	packetCount := 0
	next := time.Now()

	for _, opusData := range packets {
		select {
		case <-p.stop:
			return packetCount, nil
		default:
		}

		if delay := time.Until(next); delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-p.stop:
				if !timer.Stop() {
					<-timer.C
				}
				return packetCount, nil
			case <-timer.C:
			}
		}

		if err := p.writeOpus(opusData); err != nil {
			return packetCount, fmt.Errorf("failed to write RTP packet: %w", err)
		}
		packetCount++
		next = next.Add(20 * time.Millisecond)
	}

	return packetCount, nil
}

func (p *AudioPlayer) Stop() {
	safeClose(p.stop)
}

func (p *AudioPlayer) IsStopped() bool {
	select {
	case <-p.stop:
		return true
	default:
		return false
	}
}

func (p *AudioPlayer) ResetAfterInterrupt() {
	p.stop = make(chan struct{})
}

func (p *AudioPlayer) PlayFileLoop(filePath string) error {
	for {
		if _, err := p.PlayFile(filePath); err != nil {
			return err
		}
		select {
		case <-p.stop:
			return nil
		default:
		}
	}
}

// PlaySilence keeps DTLS-SRTP/RTP active while IVR waits for input. The packet
// is the standard minimal Opus silence frame and uses the same RTP timeline as
// prompts and bridge hand-offs.
func (p *AudioPlayer) PlaySilence(duration time.Duration) {
	silence := []byte{0xF8, 0xFF, 0xFE}
	if duration <= 0 {
		return
	}

	deadline := time.Now().Add(duration)
	next := time.Now()
	for time.Now().Before(deadline) {
		select {
		case <-p.stop:
			return
		default:
		}

		if delay := time.Until(next); delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-p.stop:
				if !timer.Stop() {
					<-timer.C
				}
				return
			case <-timer.C:
			}
		}

		if err := p.writeOpus(silence); err != nil {
			return
		}
		next = next.Add(20 * time.Millisecond)
	}
}

// Prime sends a short RTP keepalive immediately after DTLS-SRTP becomes
// connected. This establishes the outbound audio stream before the IVR graph
// performs JSON/path work and makes answer-to-audio behavior deterministic.
func (p *AudioPlayer) Prime(frames int) error {
	if frames <= 0 {
		frames = 3
	}
	silence := []byte{0xF8, 0xFF, 0xFE}
	for i := 0; i < frames; i++ {
		if i > 0 {
			time.Sleep(20 * time.Millisecond)
		}
		if err := p.writeOpus(silence); err != nil {
			return err
		}
	}
	return nil
}

// readOpusPackets parses OGG pages into individual Opus packets. OpusHead and
// OpusTags packets are identified by signature rather than assuming each lives
// on exactly one page, which makes uploaded assets with different muxers safe.
func readOpusPackets(r io.Reader) ([][]byte, error) {
	const oggPageHeaderLen = 27
	var packets [][]byte
	var continued []byte

	for {
		header := make([]byte, oggPageHeaderLen)
		if _, err := io.ReadFull(r, header); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				break
			}
			return nil, fmt.Errorf("failed to read page header: %w", err)
		}
		if string(header[0:4]) != "OggS" {
			return nil, fmt.Errorf("invalid OGG page signature")
		}

		segmentsCount := int(header[26])
		segTable := make([]byte, segmentsCount)
		if _, err := io.ReadFull(r, segTable); err != nil {
			return nil, fmt.Errorf("failed to read segment table: %w", err)
		}

		payloadSize := 0
		for _, s := range segTable {
			payloadSize += int(s)
		}
		payload := make([]byte, payloadSize)
		if _, err := io.ReadFull(r, payload); err != nil {
			return nil, fmt.Errorf("failed to read page payload: %w", err)
		}

		offset := 0
		current := continued
		continued = nil
		for _, segSize := range segTable {
			sz := int(segSize)
			if offset+sz > len(payload) {
				return nil, fmt.Errorf("invalid OGG lacing table")
			}
			current = append(current, payload[offset:offset+sz]...)
			offset += sz

			if segSize < 255 {
				if len(current) > 0 && !isOpusHeaderPacket(current) {
					pkt := append([]byte(nil), current...)
					packets = append(packets, pkt)
				}
				current = nil
			}
		}
		if len(current) > 0 {
			continued = current
		}
	}

	if len(continued) > 0 && !isOpusHeaderPacket(continued) {
		packets = append(packets, append([]byte(nil), continued...))
	}
	return packets, nil
}

func isOpusHeaderPacket(packet []byte) bool {
	if len(packet) >= 8 && string(packet[:8]) == "OpusHead" {
		return true
	}
	if len(packet) >= 8 && string(packet[:8]) == "OpusTags" {
		return true
	}
	return false
}
