package main

import "testing"

func TestParsePortRange(t *testing.T) {
	cases := []struct {
		in       string
		min, max uint16
		ok       bool
	}{
		{"", 0, 0, true},
		{"50000-50100", 50000, 50100, true},
		{" 40000 - 40000 ", 40000, 40000, true},
		{"50100-50000", 0, 0, false},
		{"0-10", 0, 0, false},
		{"50000", 0, 0, false},
		{"a-b", 0, 0, false},
		{"1-70000", 0, 0, false},
	}
	for _, c := range cases {
		lo, hi, err := parsePortRange(c.in)
		if (err == nil) != c.ok || lo != c.min || hi != c.max {
			t.Errorf("parsePortRange(%q) = %d, %d, %v", c.in, lo, hi, err)
		}
	}
}

func TestSplitList(t *testing.T) {
	got := splitList(" stun:a:3478 , ,turn:b:3478,")
	if len(got) != 2 || got[0] != "stun:a:3478" || got[1] != "turn:b:3478" {
		t.Errorf("splitList = %q", got)
	}
	if splitList("") != nil {
		t.Error("empty input should give no items")
	}
}

func TestParseFlags(t *testing.T) {
	t.Setenv("AMBIVOX_LISTEN", ":9999")
	t.Setenv("AMBIVOX_MAX_ROOMS", "7")
	o, err := parseFlags([]string{"-max-rooms", "9", "-ice-loopback"})
	if err != nil {
		t.Fatal(err)
	}
	if o.listen != ":9999" || o.maxRooms != 9 || !o.iceLoopback {
		t.Errorf("options = %+v", o)
	}

	if _, err := parseFlags([]string{"-tls-cert", "cert.pem"}); err == nil {
		t.Error("certificate without key accepted")
	}
	if _, err := parseFlags([]string{"stray"}); err == nil {
		t.Error("stray argument accepted")
	}
	t.Setenv("AMBIVOX_MAX_ROOMS", "many")
	if _, err := parseFlags(nil); err == nil {
		t.Error("bad AMBIVOX_MAX_ROOMS accepted")
	}
}

func TestNewLogger(t *testing.T) {
	if _, err := newLogger("debug", "json"); err != nil {
		t.Error(err)
	}
	if _, err := newLogger("loud", "text"); err == nil {
		t.Error("bad level accepted")
	}
	if _, err := newLogger("info", "xml"); err == nil {
		t.Error("bad format accepted")
	}
}
