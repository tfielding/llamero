package wol

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"
)

func TestParseMAC(t *testing.T) {
	if _, err := ParseMAC("aa:bb:cc:dd:ee:ff"); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseMAC("AA-BB-CC-DD-EE-FF"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "nope", "aa:bb:cc:dd:ee", "00:00:00:00:fe:80:00:00:00:00:00:00:02:00:5e:10:00:00:00:01"} {
		if _, err := ParseMAC(bad); err == nil {
			t.Errorf("ParseMAC(%q) should fail", bad)
		}
	}
}

func TestSend(t *testing.T) {
	ln, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	mac, _ := ParseMAC("aa:bb:cc:dd:ee:ff")
	if err := Send(context.Background(), mac, ln.LocalAddr().String()); err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, 200)
	_ = ln.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err := ln.ReadFrom(buf)
	if err != nil {
		t.Fatal(err)
	}
	got := buf[:n]
	if len(got) != 102 || !bytes.Equal(got[:6], bytes.Repeat([]byte{0xFF}, 6)) {
		t.Fatalf("bad header or length %d: %x", len(got), got)
	}
	for i := range 16 {
		if !bytes.Equal(got[6+i*6:12+i*6], mac) {
			t.Fatalf("repetition %d is %x", i, got[6+i*6:12+i*6])
		}
	}
}
