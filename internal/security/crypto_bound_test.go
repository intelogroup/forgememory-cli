package security

import "testing"

func TestEncryptBoundRejectsContextRebinding(t *testing.T) {
	key := []byte("test-key-not-random-32-bytes!!!!")
	sealed, err := EncryptBound(key, "private memory", "owner:one|boundary:alpha|revision:r1")
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecryptBound(key, sealed, "owner:one|boundary:alpha|revision:r1")
	if err != nil || got != "private memory" {
		t.Fatalf("round trip = %q, %v", got, err)
	}
	if _, err := DecryptBound(key, sealed, "owner:one|boundary:beta|revision:r1"); err == nil {
		t.Fatal("context rebinding decrypted successfully")
	}
}
