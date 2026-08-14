package ipc

import (
	"encoding/json"
	"net"
	"testing"
)

func TestAwaitAckAccepted(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	go func() {
		_ = json.NewEncoder(server).Encode(map[string]string{"type": "ack", "status": "accepted"})
	}()

	if err := awaitAck(client); err != nil {
		t.Fatalf("awaitAck: %v", err)
	}
}

func TestAwaitAckRejected(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	go func() {
		_ = json.NewEncoder(server).Encode(map[string]string{"type": "ack", "status": "rejected", "error": "insert failed"})
	}()

	if err := awaitAck(client); err == nil {
		t.Fatal("awaitAck unexpectedly accepted rejection")
	}
}
