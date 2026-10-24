package kafka

import (
	"testing"
	"time"
)

// Reader.Close waits for the in-flight fetch: with five consumers the default
// MaxWait (10s) would blow the graceful-stop budget, so it stays short.
func TestReaderConfigKeepsFetchesShortAndCommitsSynchronously(t *testing.T) {
	cfg := readerConfig([]string{"b:9092"}, "topic", "group")
	if cfg.MaxWait != 500*time.Millisecond {
		t.Errorf("MaxWait = %v, want 500ms", cfg.MaxWait)
	}
	if cfg.Topic != "topic" || cfg.GroupID != "group" || len(cfg.Brokers) != 1 || cfg.CommitInterval != 0 {
		t.Errorf("config = %+v", cfg)
	}
}
