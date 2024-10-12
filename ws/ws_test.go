package ws

import (
	"testing"
)

func TestGenerateSelfSignedCert(t *testing.T) {
	host := "localhost"
	cert, err := GenerateSelfSignedCert(host)
	if err != nil {
		t.Fatalf("Failed to generate self-signed certificate: %v", err)
	}
	if len(cert.Certificate) == 0 {
		t.Fatalf("Certificate is empty")
	}
	t.Logf("Generated certificate: %+v", cert)
}

func TestGenerateSelfSignedCertWithECC(t *testing.T) {
	host := "localhost"
	cert, err := GenerateSelfSignedCert(host, WithECC())
	if err != nil {
		t.Fatalf("Failed to generate self-signed certificate: %v", err)
	}
	if len(cert.Certificate) == 0 {
		t.Fatalf("Certificate is empty")
	}
	t.Logf("Generated certificate: %+v", cert)
}
