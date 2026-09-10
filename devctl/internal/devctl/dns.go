package devctl

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

// Probe the DNS forwarding path itself. A listening local SOCKS socket alone does
// not establish that the authenticated upstream tunnel is still usable.
func probeDNS(ctx context.Context, address, name string) error {
	query := make([]byte, 12)
	if _, err := rand.Read(query[:2]); err != nil {
		return err
	}
	query[2] = 1
	query[5] = 1
	for _, label := range strings.Split(strings.TrimSuffix(name, "."), ".") {
		if len(label) == 0 || len(label) > 63 {
			return fmt.Errorf("invalid DNS name")
		}
		query = append(query, byte(len(label)))
		query = append(query, label...)
	}
	query = append(query, 0, 0, 1, 0, 1)
	d := net.Dialer{Timeout: 2 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("DNS tunnel is unavailable")
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	packet := make([]byte, 2+len(query))
	binary.BigEndian.PutUint16(packet, uint16(len(query)))
	copy(packet[2:], query)
	if _, err = conn.Write(packet); err != nil {
		return fmt.Errorf("DNS tunnel write failed")
	}
	var length [2]byte
	if _, err = io.ReadFull(conn, length[:]); err != nil {
		return fmt.Errorf("DNS tunnel read failed")
	}
	n := int(binary.BigEndian.Uint16(length[:]))
	if n < 12 {
		return fmt.Errorf("invalid DNS response")
	}
	reply := make([]byte, n)
	if _, err = io.ReadFull(conn, reply); err != nil {
		return fmt.Errorf("incomplete DNS response")
	}
	if binary.BigEndian.Uint16(reply) != binary.BigEndian.Uint16(query) || reply[2]&0x80 == 0 || reply[3]&0x0f != 0 || binary.BigEndian.Uint16(reply[6:8]) == 0 {
		return fmt.Errorf("cluster DNS returned no successful answer")
	}
	return nil
}
