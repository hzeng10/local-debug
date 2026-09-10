package provision

import (
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/pem"
	"fmt"
	"strings"
)

func keyFingerprint(b []byte) (string, error) {
	var der []byte
	var e error
	s := strings.TrimSpace(string(b))
	if strings.HasPrefix(s, "ck-") {
		der, e = base64.RawStdEncoding.DecodeString(s[3:])
		if e != nil {
			return "", e
		}
	} else {
		p, _ := pem.Decode(b)
		if p == nil {
			return "", fmt.Errorf("invalid Chisel key file")
		}
		der = p.Bytes
	}
	key, e := x509.ParseECPrivateKey(der)
	if e != nil {
		return "", fmt.Errorf("invalid Chisel EC key")
	}
	if key.Curve != elliptic.P256() {
		return "", fmt.Errorf("Chisel key must use P-256")
	}
	wire := []byte{}
	for _, s := range [][]byte{[]byte("ecdsa-sha2-nistp256"), []byte("nistp256"), elliptic.Marshal(key.Curve, key.X, key.Y)} {
		n := make([]byte, 4)
		binary.BigEndian.PutUint32(n, uint32(len(s)))
		wire = append(wire, n...)
		wire = append(wire, s...)
	}
	sum := sha256.Sum256(wire)
	return base64.StdEncoding.EncodeToString(sum[:]), nil
}
