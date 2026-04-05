package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
	"time"
)

func TestVerifyStripeSignature_Valid(t *testing.T) {
	secret := "whsec_test"
	body := []byte(`{"type":"checkout.session.completed"}`)
	ts := fmt.Sprintf("%d", time.Now().Unix())
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "." + string(body)))
	sig := hex.EncodeToString(mac.Sum(nil))

	header := fmt.Sprintf("t=%s,v1=%s", ts, sig)
	if !verifyStripeSignature(header, body, secret) {
		t.Fatal("verifyStripeSignature returned false for valid signature")
	}
}

func TestVerifyStripeSignature_Invalid(t *testing.T) {
	secret := "whsec_test"
	body := []byte(`{"type":"checkout.session.completed"}`)
	ts := fmt.Sprintf("%d", time.Now().Unix())
	header := fmt.Sprintf("t=%s,v1=%s", ts, "deadbeef")

	if verifyStripeSignature(header, body, secret) {
		t.Fatal("verifyStripeSignature returned true for invalid signature")
	}
}

func TestVerifyStripeSignature_StaleTimestamp(t *testing.T) {
	secret := "whsec_test"
	body := []byte(`{"type":"checkout.session.completed"}`)
	oldTS := fmt.Sprintf("%d", time.Now().Add(-10*time.Minute).Unix())
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(oldTS + "." + string(body)))
	sig := hex.EncodeToString(mac.Sum(nil))

	header := fmt.Sprintf("t=%s,v1=%s", oldTS, sig)
	if verifyStripeSignature(header, body, secret) {
		t.Fatal("verifyStripeSignature returned true for stale signature")
	}
}
