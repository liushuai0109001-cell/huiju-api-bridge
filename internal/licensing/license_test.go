package licensing

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"
)

func TestSignedLicenseVerification(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	code, err := Sign(privateKey, Claims{Product: Product, LicenseID: "test", MachineCode: "HJ-TEST", IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(code, publicKey, "HJ-TEST", now); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(code, publicKey, "HJ-OTHER", now); err == nil {
		t.Fatal("license unexpectedly worked on another machine")
	}
	if _, err := Verify(code, publicKey, "HJ-TEST", now.Add(2*time.Hour)); err == nil {
		t.Fatal("expired license unexpectedly worked")
	}
}
