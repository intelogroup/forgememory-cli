package main

import "testing"

func TestParseRecallArgs(t *testing.T) {
	got, err := parseRecallArgs([]string{"user", "interrupts", "--all", "--limit", "3", "--json"})
	if err != nil || got.query != "user interrupts" || !got.all || !got.json || got.limit != 3 {
		t.Fatalf("got %+v err=%v", got, err)
	}
	got, err = parseRecallArgs([]string{"--project", "wifizone", "routing"})
	if err != nil || got.project != "wifizone" || got.query != "routing" || got.limit != 5 {
		t.Fatalf("got %+v err=%v", got, err)
	}
	for _, bad := range [][]string{{}, {"--all"}, {"q", "--limit"}, {"q", "--limit", "0"}, {"q", "--limit", "x"}, {"q", "--project"}} {
		if _, err := parseRecallArgs(bad); err == nil {
			t.Errorf("parseRecallArgs(%v) accepted", bad)
		}
	}
}
