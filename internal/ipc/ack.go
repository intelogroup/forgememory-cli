package ipc

import (
	"encoding/json"
	"fmt"
	"net"
)

func awaitAck(conn net.Conn) error {
	var ack struct {
		Type   string `json:"type"`
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	if err := json.NewDecoder(conn).Decode(&ack); err != nil {
		return fmt.Errorf("read daemon acknowledgement: %w", err)
	}
	if ack.Type != "ack" || ack.Status != "accepted" {
		if ack.Error != "" {
			return fmt.Errorf("daemon rejected event: %s", ack.Error)
		}
		return fmt.Errorf("daemon returned invalid acknowledgement")
	}
	return nil
}
