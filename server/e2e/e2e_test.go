//go:build e2e

// Package e2e drives a complete call: two WebRTC participants, the Go server
// and the real media engine binary running its simulated backend.
package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/whispem/ambivox/server/internal/httpapi"
	"github.com/whispem/ambivox/server/internal/hub"
	"github.com/whispem/ambivox/server/internal/medialink"
	"github.com/whispem/ambivox/server/internal/metrics"
	"github.com/whispem/ambivox/server/internal/testclient"
	"github.com/whispem/ambivox/server/web"
)

const frame = 20 * time.Millisecond

type harness struct {
	t      *testing.T
	ctx    context.Context
	bin    string
	dir    string
	server *httptest.Server
}

func start(t *testing.T) *harness {
	t.Helper()
	bin := os.Getenv("AMBIVOX_MEDIA_BIN")
	if bin == "" {
		t.Fatal("set AMBIVOX_MEDIA_BIN to the ambivox-media binary (see `make e2e`)")
	}
	dir, err := os.MkdirTemp("", "ambivox-e2e")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	socket := filepath.Join(dir, "media.sock")

	engine := exec.CommandContext(ctx, bin, "serve", "--socket", socket)
	engine.Stderr = os.Stderr
	if err := engine.Start(); err != nil {
		t.Fatal(err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	link := medialink.Dial(ctx, socket, log)
	registry := metrics.NewRegistry()
	rooms, err := hub.New(hub.Config{Link: link, Logger: log, Metrics: registry, IncludeLoopback: true, DisableMDNS: true})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpapi.New(rooms, link, registry, web.Static()))
	t.Cleanup(func() {
		rooms.Close()
		server.Close()
		cancel()
		<-link.Done()
		_ = engine.Wait()
		os.RemoveAll(dir)
	})

	h := &harness{t: t, ctx: ctx, bin: bin, dir: dir, server: server}
	h.eventually("the media engine", func() bool {
		conn, err := net.Dial("unix", socket)
		if err == nil {
			conn.Close()
		}
		return err == nil && link.Connected()
	})
	return h
}

func (h *harness) eventually(what string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			h.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (h *harness) media(args ...string) string {
	h.t.Helper()
	var stderr bytes.Buffer
	cmd := exec.CommandContext(h.ctx, h.bin, args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		h.t.Fatalf("ambivox-media %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return strings.TrimSpace(string(out))
}

func (h *harness) speak(text, lang string) [][]byte {
	h.t.Helper()
	path := filepath.Join(h.dir, "said-"+lang+".opus")
	h.media("synth", "--text", text, "--lang", lang, "--out", path)
	file, err := os.Open(path)
	if err != nil {
		h.t.Fatal(err)
	}
	defer file.Close()

	var packets [][]byte
	reader := bufio.NewReader(file)
	for {
		var size uint16
		if err := binary.Read(reader, binary.BigEndian, &size); err != nil {
			break
		}
		packet := make([]byte, size)
		if _, err := io.ReadFull(reader, packet); err != nil {
			h.t.Fatal(err)
		}
		packets = append(packets, packet)
	}
	if len(packets) == 0 {
		h.t.Fatal("synth produced no audio")
	}
	return packets
}

func (h *harness) understand(packets [][]byte, lang string) string {
	h.t.Helper()
	var file bytes.Buffer
	for _, packet := range packets {
		_ = binary.Write(&file, binary.BigEndian, uint16(len(packet)))
		file.Write(packet)
	}
	path := filepath.Join(h.dir, "heard-"+lang+".opus")
	if err := os.WriteFile(path, file.Bytes(), 0o600); err != nil {
		h.t.Fatal(err)
	}
	return h.media("listen", "--lang", lang, "--in", path)
}

func (h *harness) join(room, lang, name string) *testclient.Client {
	h.t.Helper()
	url := "ws" + strings.TrimPrefix(h.server.URL, "http") + "/ws"
	client, _, err := testclient.Join(h.ctx, url, room, lang, name)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(client.Close)
	if err := client.Connect(h.ctx); err != nil {
		h.t.Fatal(err)
	}
	return client
}

func (h *harness) caption(client *testclient.Client, kind string) string {
	h.t.Helper()
	for {
		event, err := client.Wait(h.ctx, "caption")
		if err != nil {
			h.t.Fatal(err)
		}
		if event.String("kind") == kind && event["final"] == true {
			return event.String("text")
		}
	}
}

// say plays packets from one participant and returns what the other one received in response.
func (h *harness) say(from, to *testclient.Client, packets [][]byte) [][]byte {
	h.t.Helper()
	before := len(to.Packets())
	ticker := time.NewTicker(frame)
	defer ticker.Stop()
	for _, packet := range packets {
		if err := from.WriteAudio(packet, frame); err != nil {
			h.t.Fatal(err)
		}
		<-ticker.C
	}

	h.eventually("translated speech", func() bool { return len(to.Packets()) > before })
	last, quietSince := len(to.Packets()), time.Now()
	for time.Since(quietSince) < 600*time.Millisecond {
		time.Sleep(50 * time.Millisecond)
		if n := len(to.Packets()); n != last {
			last, quietSince = n, time.Now()
		}
	}
	return to.Packets()[before:]
}

func TestTranslatedCallEndToEnd(t *testing.T) {
	h := start(t)
	alice := h.join("e2e", "en", "Alice")
	bob := h.join("e2e", "fr", "Bob")

	const said, translated = "hello world and friend and water", "bonjour monde et ami et eau"
	heard := h.say(alice, bob, h.speak(said, "en"))
	if got := h.caption(bob, "source"); got != said {
		t.Errorf("source caption = %q", got)
	}
	if got := h.caption(alice, "translation"); got != translated {
		t.Errorf("translation caption = %q", got)
	}
	if got := h.understand(heard, "fr"); got != translated {
		t.Errorf("Bob heard %q, want %q", got, translated)
	}

	var drafts []string
	for _, event := range bob.Events("caption") {
		if event["final"] == false && event.String("kind") == "source" {
			drafts = append(drafts, event.String("text"))
		}
	}
	if len(drafts) == 0 || drafts[0] == said {
		t.Errorf("no live caption reached the listener before the sentence ended: %q", drafts)
	}
	for _, draft := range drafts {
		if !strings.HasPrefix(said, draft) {
			t.Errorf("live caption %q is not a prefix of %q", draft, said)
		}
	}

	heard = h.say(bob, alice, h.speak("merci ami", "fr"))
	if got := h.caption(alice, "translation"); got != "thanks friend" {
		t.Errorf("translation caption = %q", got)
	}
	if got := h.understand(heard, "en"); got != "thanks friend" {
		t.Errorf("Alice heard %q, want %q", got, "thanks friend")
	}

	utterances := regexp.MustCompile(`(?m)^ambivox_engine_utterances (\d+)$`)
	h.eventually("engine statistics", func() bool {
		resp, err := http.Get(h.server.URL + "/metrics")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		match := utterances.FindSubmatch(body)
		if match == nil {
			return false
		}
		n, _ := strconv.Atoi(string(match[1]))
		return n >= 2
	})
}

func TestSameLanguageCallBypassesTheEngine(t *testing.T) {
	h := start(t)
	alice := h.join("e2e-direct", "es", "Alice")
	bob := h.join("e2e-direct", "es", "Bob")

	heard := h.say(alice, bob, h.speak("hola amigo", "es"))
	if got := h.understand(heard, "es"); got != "hola amigo" {
		t.Errorf("Bob heard %q, want the untouched original", got)
	}
	if captions := bob.Events("caption"); len(captions) != 0 {
		t.Errorf("unexpected captions: %v", captions)
	}
}
