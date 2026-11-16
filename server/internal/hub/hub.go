// Package hub owns rooms, peers and their WebRTC sessions, and wires each
// speaker to the media engine stream that translates for the listener.
package hub

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/pion/ice/v4"
	"github.com/pion/interceptor"
	"github.com/pion/webrtc/v4"

	"github.com/whispem/ambivox/server/internal/medialink"
	"github.com/whispem/ambivox/server/internal/metrics"
)

var Languages = []string{"en", "fr", "es", "it"}

const (
	roomCapacity = 2
	maxNameRunes = 40
)

var (
	ErrRoomFull     = errors.New("this room already has two participants")
	ErrTooManyRooms = errors.New("the server has reached its room limit")
	ErrBadRoom      = errors.New("room names use 1-64 letters, digits, '-' or '_'")
	ErrBadLanguage  = errors.New("unsupported language")
	ErrShuttingDown = errors.New("the server is shutting down")

	roomPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

type Config struct {
	Link     *medialink.Link
	Logger   *slog.Logger
	Metrics  *metrics.Registry
	MaxRooms int

	ICEServers      []string
	PublicIPs       []string
	UDPPortMin      uint16
	UDPPortMax      uint16
	IncludeLoopback bool
	DisableMDNS     bool
	AllowedOrigins  []string
}

type Hub struct {
	cfg  Config
	api  *webrtc.API
	log  *slog.Logger
	ctx  context.Context
	stop context.CancelFunc

	mu       sync.Mutex
	rooms    map[string]*Room
	sessions sync.WaitGroup

	sessionsTotal *metrics.Counter
	packetsIn     *metrics.Counter
	packetsDirect *metrics.Counter
	packetsEngine *metrics.Counter
	speechOut     *metrics.Counter
	captions      *metrics.Counter
}

func New(cfg Config) (*Hub, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Metrics == nil {
		cfg.Metrics = metrics.NewRegistry()
	}
	if cfg.MaxRooms <= 0 {
		cfg.MaxRooms = 100
	}

	media := &webrtc.MediaEngine{}
	err := media.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: opusCapability,
		PayloadType:        111,
	}, webrtc.RTPCodecTypeAudio)
	if err != nil {
		return nil, err
	}
	interceptors := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(media, interceptors); err != nil {
		return nil, err
	}

	settings := webrtc.SettingEngine{LoggerFactory: pionLogs{cfg.Logger}}
	settings.SetIncludeLoopbackCandidate(cfg.IncludeLoopback)
	if cfg.DisableMDNS {
		settings.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
	}
	if len(cfg.PublicIPs) > 0 {
		settings.SetNAT1To1IPs(cfg.PublicIPs, webrtc.ICECandidateTypeHost)
	}
	if cfg.UDPPortMin != 0 || cfg.UDPPortMax != 0 {
		if err := settings.SetEphemeralUDPPortRange(cfg.UDPPortMin, cfg.UDPPortMax); err != nil {
			return nil, fmt.Errorf("udp port range: %w", err)
		}
	}

	ctx, stop := context.WithCancel(context.Background())
	h := &Hub{
		cfg: cfg,
		api: webrtc.NewAPI(
			webrtc.WithMediaEngine(media),
			webrtc.WithInterceptorRegistry(interceptors),
			webrtc.WithSettingEngine(settings),
		),
		log:   cfg.Logger,
		ctx:   ctx,
		stop:  stop,
		rooms: make(map[string]*Room),
	}

	m := cfg.Metrics
	h.sessionsTotal = m.Counter("ambivox_sessions_total", "Signalling sessions accepted.")
	h.packetsIn = m.Counter("ambivox_rtp_packets_received_total", "RTP packets received from participants.")
	h.packetsDirect = m.Counter("ambivox_rtp_packets_relayed_total", "Packets relayed untouched between same-language participants.")
	h.packetsEngine = m.Counter("ambivox_rtp_packets_translated_total", "Packets handed to the media engine.")
	h.speechOut = m.Counter("ambivox_speech_packets_sent_total", "Translated speech packets sent to participants.")
	h.captions = m.Counter("ambivox_captions_total", "Captions delivered to participants.")
	m.Gauge("ambivox_rooms", "Open rooms.", func() float64 { return float64(h.Rooms()) })
	m.Gauge("ambivox_peers", "Connected participants.", func() float64 { return float64(h.Peers()) })
	m.Gauge("ambivox_media_connected", "Whether the media engine link is up.", func() float64 {
		if cfg.Link.Connected() {
			return 1
		}
		return 0
	})
	m.Gauge("ambivox_media_frames_dropped", "Frames dropped because the media engine link was saturated.", func() float64 {
		return float64(cfg.Link.Dropped())
	})
	m.Collect(func() map[string]float64 {
		out := make(map[string]float64)
		for name, value := range cfg.Link.Stats() {
			out["ambivox_engine_"+name] = value
		}
		return out
	})

	cfg.Link.OnChange(h.mediaChanged)
	return h, nil
}

func (h *Hub) Close() {
	h.stop()
	h.sessions.Wait()
}

func (h *Hub) Rooms() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.rooms)
}

func (h *Hub) Peers() int {
	h.mu.Lock()
	rooms := make([]*Room, 0, len(h.rooms))
	for _, room := range h.rooms {
		rooms = append(rooms, room)
	}
	h.mu.Unlock()

	total := 0
	for _, room := range rooms {
		room.mu.Lock()
		total += len(room.peers)
		room.mu.Unlock()
	}
	return total
}

func (h *Hub) iceServers() []webrtc.ICEServer {
	if len(h.cfg.ICEServers) == 0 {
		return nil
	}
	return []webrtc.ICEServer{{URLs: h.cfg.ICEServers}}
}

func (h *Hub) join(roomID, lang, name string) (*Peer, error) {
	if !roomPattern.MatchString(roomID) {
		return nil, ErrBadRoom
	}
	if !slices.Contains(Languages, lang) {
		return nil, fmt.Errorf("%w: %q", ErrBadLanguage, lang)
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ctx.Err() != nil {
		return nil, ErrShuttingDown
	}
	room := h.rooms[roomID]
	if room == nil {
		if len(h.rooms) >= h.cfg.MaxRooms {
			return nil, ErrTooManyRooms
		}
		room = &Room{id: roomID, hub: h}
	}

	room.mu.Lock()
	defer room.mu.Unlock()
	if len(room.peers) >= roomCapacity {
		return nil, ErrRoomFull
	}
	peer := &Peer{
		ID:   newID(),
		Name: cleanName(name),
		Lang: lang,
		room: room,
		hub:  h,
		send: make(chan []byte, sendQueue),
	}
	var other *Peer
	if len(room.peers) == 1 {
		other = room.peers[0]
	}
	room.peers = append(room.peers, peer)
	h.rooms[roomID] = room
	room.rewire()

	connected := h.cfg.Link.Connected()
	welcome := message{
		Type:      "joined",
		ID:        peer.ID,
		Room:      roomID,
		Lang:      peer.Lang,
		Name:      peer.Name,
		Connected: &connected,
		Backend:   h.cfg.Link.Backend(),
		Languages: Languages,
	}
	if len(h.cfg.ICEServers) > 0 {
		welcome.ICEServers = []iceEntry{{URLs: h.cfg.ICEServers}}
	}
	if other != nil {
		welcome.Peer = other.info()
		other.notify(message{Type: "peer-joined", Peer: peer.info()})
	}
	peer.notify(welcome)
	h.log.Info("peer joined", "room", roomID, "peer", peer.ID, "lang", lang)
	return peer, nil
}

func (h *Hub) leave(peer *Peer) {
	room := peer.room
	h.mu.Lock()
	room.mu.Lock()
	index := slices.Index(room.peers, peer)
	if index >= 0 {
		room.peers = slices.Delete(room.peers, index, index+1)
		if peer.stream != nil {
			go peer.stream.Close()
			peer.stream, peer.streamTo = nil, nil
		}
		room.rewire()
		for _, other := range room.peers {
			other.notify(message{Type: "peer-left", ID: peer.ID})
		}
		if len(room.peers) == 0 {
			delete(h.rooms, room.id)
		}
	}
	room.mu.Unlock()
	h.mu.Unlock()

	if index >= 0 {
		h.log.Info("peer left", "room", room.id, "peer", peer.ID)
	}
	peer.closeConnection()
}

func (h *Hub) mediaChanged(connected bool) {
	h.mu.Lock()
	rooms := make([]*Room, 0, len(h.rooms))
	for _, room := range h.rooms {
		rooms = append(rooms, room)
	}
	h.mu.Unlock()

	status := message{Type: "media", Connected: &connected, Backend: h.cfg.Link.Backend()}
	for _, room := range rooms {
		room.mu.Lock()
		if connected {
			room.rewire()
		}
		for _, peer := range room.peers {
			peer.notify(status)
		}
		room.mu.Unlock()
	}
}

type Room struct {
	id  string
	hub *Hub

	mu    sync.Mutex
	peers []*Peer
}

func (r *Room) other(p *Peer) *Peer {
	for _, candidate := range r.peers {
		if candidate != p {
			return candidate
		}
	}
	return nil
}

// rewire makes each peer's engine stream match the current pairing. The caller holds r.mu.
func (r *Room) rewire() {
	for _, p := range r.peers {
		other := r.other(p)
		translate := other != nil && other.Lang != p.Lang
		if p.stream != nil && (!translate || p.streamTo != other) {
			go p.stream.Close()
			p.stream, p.streamTo = nil, nil
		}
		if !translate || p.stream != nil {
			continue
		}
		b := &bridge{room: r, from: p, to: other}
		stream, err := r.hub.cfg.Link.Open(p.Lang, other.Lang, b)
		if err != nil {
			r.hub.log.Warn("cannot open translation stream", "room", r.id, "peer", p.ID, "error", err)
			continue
		}
		b.stream = stream
		p.stream, p.streamTo = stream, other
	}
}

func (r *Room) route(from *Peer, sequence uint16, payload []byte) {
	r.mu.Lock()
	other, stream := r.other(from), from.stream
	r.mu.Unlock()
	r.hub.packetsIn.Inc()

	switch {
	case other == nil:
	case other.Lang == from.Lang:
		other.writeSample(payload)
		r.hub.packetsDirect.Inc()
	case stream != nil:
		stream.Write(sequence, payload)
		r.hub.packetsEngine.Inc()
	}
}

type bridge struct {
	room   *Room
	from   *Peer
	to     *Peer
	stream *medialink.Stream
}

func (b *bridge) OnSpeech(packet []byte) {
	b.to.writeSample(packet)
	b.room.hub.speechOut.Inc()
}

func (b *bridge) OnCaption(kind captionKind, utterance uint32, text string) {
	caption := message{Type: "caption", From: b.from.ID, Kind: kind.String(), Utterance: &utterance, Text: text}
	b.from.notify(caption)
	b.to.notify(caption)
	b.room.hub.captions.Add(2)
}

func (b *bridge) OnError(text string) {
	b.room.hub.log.Warn("translation error", "room", b.room.id, "peer", b.from.ID, "error", text)
	b.from.notify(message{Type: "error", Message: text})
}

func (b *bridge) OnClosed() {
	b.room.mu.Lock()
	defer b.room.mu.Unlock()
	if b.from.stream == b.stream {
		b.from.stream, b.from.streamTo = nil, nil
	}
}

func newID() string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(raw[:])
}

func cleanName(name string) string {
	name = strings.Join(strings.Fields(name), " ")
	if utf8.RuneCountInString(name) > maxNameRunes {
		name = string([]rune(name)[:maxNameRunes])
	}
	if name == "" {
		return "Guest"
	}
	return name
}
