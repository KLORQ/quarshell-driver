package certbundle

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"fmt"
)

// AppendPEM parses every block in a PEM bundle and adds certificate roots to
// the pool. Unlike AppendCertsFromPEM, it rejects trailing garbage and invalid
// or non-certificate PEM blocks instead of silently accepting a partial file.
func AppendPEM(pool *x509.CertPool, bundle []byte) error {
	if pool == nil {
		return fmt.Errorf("certificate pool is nil")
	}
	remaining := bytes.TrimSpace(bundle)
	count := 0
	for len(remaining) > 0 {
		block, rest := pem.Decode(remaining)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			return fmt.Errorf("invalid certificate PEM block")
		}
		certificates, err := x509.ParseCertificates(block.Bytes)
		if err != nil || len(certificates) == 0 {
			return fmt.Errorf("invalid certificate in PEM bundle")
		}
		for _, certificate := range certificates {
			pool.AddCert(certificate)
			count++
		}
		remaining = bytes.TrimSpace(rest)
	}
	if count == 0 {
		return fmt.Errorf("PEM bundle contains no certificates")
	}
	return nil
}
