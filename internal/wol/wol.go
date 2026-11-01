// Package wol sends Wake-on-LAN magic packets.
package wol

import (
	"bytes"
	"context"
	"fmt"
	"net"
)

// DefaultAddr is the limited broadcast address on the usual WoL port.
const DefaultAddr = "255.255.255.255:9"

// ParseMAC parses a 6-byte MAC address such as aa:bb:cc:dd:ee:ff.
func ParseMAC(s string) (net.HardwareAddr, error) {
	mac, err := net.ParseMAC(s)
	if err != nil {
		return nil, err
	}
	if len(mac) != 6 {
		return nil, fmt.Errorf("%q is not a 6-byte MAC address", s)
	}
	return mac, nil
}

// Packet is the magic packet for mac: 6 bytes of 0xFF, then the MAC 16 times.
func Packet(mac net.HardwareAddr) []byte {
	return append(bytes.Repeat([]byte{0xFF}, 6), bytes.Repeat(mac, 16)...)
}

// Send sends the magic packet for mac to addr (host:port) over UDP.
// It is sent 3 times, since UDP broadcasts can be dropped.
func Send(ctx context.Context, mac net.HardwareAddr, addr string) error {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "udp", addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	p := Packet(mac)
	for range 3 {
		if _, err := conn.Write(p); err != nil {
			return err
		}
	}
	return nil
}
