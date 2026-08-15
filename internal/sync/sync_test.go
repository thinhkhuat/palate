package sync

import "testing"

func TestEncryptRoundTrip(t *testing.T) {
	key := DeriveKey("test-passphrase", "test-salt-12345678")
	ct, err := Encrypt([]byte("hello palate"), key)
	if err != nil {
		t.Fatal(err)
	}
	pt, err := Decrypt(ct, key)
	if err != nil {
		t.Fatal(err)
	}
	if string(pt) != "hello palate" {
		t.Fatalf("decrypt: %q", string(pt))
	}
}

func TestEventHashDeterministic(t *testing.T) {
	a := EventHash("id1", "create", "2026-01-01T00:00:00Z", "dev1", "payload", nil, 0.5)
	b := EventHash("id1", "create", "2026-01-01T00:00:00Z", "dev1", "payload", nil, 0.5)
	if a != b {
		t.Fatalf("hash not deterministic: %q vs %q", a, b)
	}
}
