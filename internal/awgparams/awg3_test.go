package awgparams

import (
	"encoding/base64"
	"testing"
)

func TestGenerateAWG3ProducesValidIndependentKeys(t *testing.T) {
	first, err := GenerateAWG3(Options{Profile: "tls_client_hello"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := GenerateAWG3(Options{Profile: "tls_client_hello"})
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(ProtocolAWG3, first); err != nil {
		t.Fatalf("generated settings are invalid: %v", err)
	}
	key, err := base64.StdEncoding.DecodeString(first.HeaderProtectionKey)
	if err != nil || len(key) != 32 {
		t.Fatalf("header key is not 32-byte Base64: %q", first.HeaderProtectionKey)
	}
	if first.HeaderProtectionKey == second.HeaderProtectionKey {
		t.Fatal("separate generators reused a header protection key")
	}
	if first.S1 < 12 || first.S2 < 12 || first.S3 < 12 || first.S4 < 12 {
		t.Fatalf("AWG 3.1 packet sizes must be >= 12: %+v", first)
	}
}

func TestValidateAWG3RejectsInvalidRangesAndKey(t *testing.T) {
	s, err := GenerateAWG3(Options{})
	if err != nil {
		t.Fatal(err)
	}
	s.HeaderProtectionKey = "invalid"
	if err := Validate(ProtocolAWG3, s); err == nil {
		t.Fatal("expected invalid key error")
	}
	s, _ = GenerateAWG3(Options{})
	s.RekeyTimeout = "7-3"
	if err := Validate(ProtocolAWG3, s); err == nil {
		t.Fatal("expected reversed range error")
	}
}
