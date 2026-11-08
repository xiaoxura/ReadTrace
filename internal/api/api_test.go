package api

import (
	"testing"
	"time"

	"readtrace/internal/store"
)

func TestMaskAndMergeConfig(t *testing.T) {
	stored := map[string]string{"bot_token": "SECRET", "chat_id": "12345", "proxy": ""}

	masked := maskConfig(stored)
	if masked["bot_token"] != maskedValue || masked["chat_id"] != maskedValue {
		t.Fatalf("real secrets leaked in masked config: %v", masked)
	}
	if masked["proxy"] != "" {
		t.Fatalf("empty values should stay empty: %v", masked)
	}

	// Sending masked values back preserves stored secrets; new values win.
	merged := mergeConfig(stored, map[string]string{
		"bot_token": maskedValue, // unchanged in UI
		"chat_id":   maskedValue, // unchanged in UI
		"proxy":     "http://127.0.0.1:7890",
	})
	if merged["bot_token"] != "SECRET" {
		t.Fatalf("stored token lost on masked round-trip: %v", merged)
	}
	if merged["proxy"] != "http://127.0.0.1:7890" {
		t.Fatalf("new value not applied: %v", merged)
	}

	// Emptying a field clears it.
	merged2 := mergeConfig(stored, map[string]string{"proxy": ""})
	if _, ok := merged2["proxy"]; ok {
		t.Fatalf("cleared field should be removed: %v", merged2)
	}
}

func TestSplitRunSecondsAcrossMidnight(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 11, 7, 23, 50, 0, 0, loc).Unix()
	run := store.Run{StartedAt: start, FinishedAt: start + 1800, ElapsedSeconds: 1800}
	got := splitRunSeconds(run, loc)
	if got["2026-11-07"] != 600 || got["2026-11-08"] != 1200 {
		t.Fatalf("split = %v, want 600/1200", got)
	}
	if total := got["2026-11-07"] + got["2026-11-08"]; total != 1800 {
		t.Fatalf("split lost seconds: %d", total)
	}

	sameDay := time.Date(2026, 11, 7, 10, 0, 0, 0, loc).Unix()
	got = splitRunSeconds(store.Run{StartedAt: sameDay, FinishedAt: sameDay + 600, ElapsedSeconds: 600}, loc)
	if got["2026-11-07"] != 600 || len(got) != 1 {
		t.Fatalf("same-day split = %v", got)
	}
}

func TestHostAllowed(t *testing.T) {
	allowed := []string{"read.example.com", "192.168.1.10"}
	cases := []struct {
		host string
		want bool
	}{
		{"read.example.com", true},
		{"read.example.com:8888", true},
		{"READ.Example.COM", true},
		{"192.168.1.10:8720", true},
		{"evil.example.com", false},
		{"192.168.1.11", false},
		{"", false},
	}
	for _, c := range cases {
		if got := hostAllowed(c.host, allowed); got != c.want {
			t.Errorf("hostAllowed(%q) = %v, want %v", c.host, got, c.want)
		}
	}
}
