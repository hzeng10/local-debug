package devctl

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"
)

func TestDNSProbeValidatesUpstreamResponse(t *testing.T) {
	for _, tc := range []struct {
		name    string
		rcode   byte
		wrongID bool
		ok      bool
	}{{"answer", 0, false, true}, {"nxdomain", 3, false, false}, {"wrong-id", 0, true, false}} {
		t.Run(tc.name, func(t *testing.T) {
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			done := make(chan struct{})
			go func() {
				defer close(done)
				conn, e := listener.Accept()
				if e != nil {
					return
				}
				defer conn.Close()
				var size [2]byte
				if _, e = io.ReadFull(conn, size[:]); e != nil {
					return
				}
				q := make([]byte, int(binary.BigEndian.Uint16(size[:])))
				if _, e = io.ReadFull(conn, q); e != nil {
					return
				}
				q[2] |= 0x80
				q[3] = 0x80 | tc.rcode
				q[7] = 1
				if tc.wrongID {
					q[0] ^= 0xff
				}
				q = append(q, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 30, 0, 4, 10, 96, 0, 1)
				binary.BigEndian.PutUint16(size[:], uint16(len(q)))
				_, _ = conn.Write(append(size[:], q...))
			}()
			err = probeDNS(context.Background(), listener.Addr().String(), "kubernetes.default.svc.cluster.local")
			<-done
			if (err == nil) != tc.ok {
				t.Fatalf("unexpected probe result: %v", err)
			}
		})
	}
}
