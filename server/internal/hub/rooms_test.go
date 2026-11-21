package hub_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/whispem/ambivox/server/internal/hub"
	"github.com/whispem/ambivox/server/internal/invite"
	"github.com/whispem/ambivox/server/internal/testclient"
)

func freeUDPPort(t *testing.T) uint16 {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	return uint16(conn.LocalAddr().(*net.UDPAddr).Port)
}

func TestInvitationsOverOneUDPPort(t *testing.T) {
	port := freeUDPPort(t)
	f := setup(t, 10, func(cfg *hub.Config) {
		cfg.Invites = invite.New([]byte("test key"), time.Hour)
		cfg.AccessKey = "open sesame"
		cfg.UDPPort = port
	})
	if !f.hub.InviteOnly() || !f.hub.AccessKeyRequired() {
		t.Fatal("invitations or access key not reported")
	}

	create := func(key string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/rooms", nil)
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		rec := httptest.NewRecorder()
		f.hub.CreateRoom(rec, req)
		return rec
	}
	for _, key := range []string{"", "open sesam"} {
		if rec := create(key); rec.Code != http.StatusUnauthorized {
			t.Errorf("access key %q: status %d", key, rec.Code)
		}
	}
	rec := create("open sesame")
	var grant struct{ Room, Token, Expires string }
	if err := json.NewDecoder(rec.Body).Decode(&grant); err != nil || rec.Code != http.StatusCreated {
		t.Fatalf("create room: %d %v", rec.Code, err)
	}
	if expires, err := time.Parse(time.RFC3339, grant.Expires); err != nil || time.Until(expires) < 59*time.Minute {
		t.Errorf("expires = %q", grant.Expires)
	}

	for _, token := range []string{"", grant.Token + "x", strings.Replace(grant.Token, ".", "0.", 1)} {
		client, err := testclient.Dial(f.ctx, f.url)
		if err != nil {
			t.Fatal(err)
		}
		_ = client.Send(testclient.Event{"type": "join", "room": grant.Room, "lang": "en", "token": token})
		if event := f.wait(client, "error"); !strings.Contains(event.String("message"), "invitation") {
			t.Errorf("token %q: %v", token, event)
		}
		if code, err := client.WaitClosed(f.ctx); err != nil || code != websocket.StatusPolicyViolation {
			t.Errorf("close code = %v, %v", code, err)
		}
	}
	if f.hub.Rooms() != 0 {
		t.Errorf("a refused join opened a room")
	}

	join := func(name string) *testclient.Client {
		client, _, err := testclient.JoinWith(f.ctx, f.url, testclient.Event{"room": grant.Room, "lang": "it", "name": name, "token": grant.Token})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(client.Close)
		return client
	}
	alice, bob := join("Alice"), join("Bob")
	f.connect(alice, bob)
	f.talk(alice, bob, 0xa1, 5)

	candidate := fmt.Sprintf(" %d typ host", port)
	for _, answer := range append(alice.Events("answer"), bob.Events("answer")...) {
		if !strings.Contains(answer.String("sdp"), candidate) {
			t.Errorf("answer does not offer UDP port %d:\n%s", port, answer.String("sdp"))
		}
	}
}

func TestOpenRoomsNeedNoToken(t *testing.T) {
	f := setup(t, 10)
	rec := httptest.NewRecorder()
	f.hub.CreateRoom(rec, httptest.NewRequest(http.MethodPost, "/rooms", nil))
	var grant map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&grant); err != nil || rec.Code != http.StatusCreated {
		t.Fatalf("create room: %d %v", rec.Code, err)
	}
	if len(grant["room"]) != 32 || grant["token"] != "" {
		t.Errorf("grant = %v", grant)
	}
	f.join(grant["room"], "en", "Alice")
}

func TestEchoGuardSilencesTheSpeakerWhileTheirTranslationPlays(t *testing.T) {
	f := setup(t, 10, func(cfg *hub.Config) { cfg.EchoGuard = true })
	alice, _ := f.join("echo", "en", "Alice")
	bob, _ := f.join("echo", "fr", "Bob")
	f.connect(alice, bob)

	marked := func() (heard, silent int) {
		for _, packet := range bob.Packets() {
			switch {
			case len(packet) == 4 && packet[1] == 0xa1:
				heard++
			case bytes.Equal(packet, []byte{0xf8, 0xff, 0xfe}):
				silent++
			}
		}
		return heard, silent
	}
	for i := range 20 {
		if err := alice.WriteAudio([]byte{0x78, 0xa1, byte(i), 0xff}, frame); err != nil {
			t.Fatal(err)
		}
		time.Sleep(frame)
	}
	f.eventually("silenced packets", func() bool { _, silent := marked(); return silent >= 10 })
	if heard, _ := marked(); heard > 5 {
		t.Errorf("Bob heard %d of Alice's packets before the guard closed", heard)
	}

	time.Sleep(time.Second)
	f.talk(alice, bob, 0xa2, 1)
}
