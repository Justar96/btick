package api

import (
	"testing"
)

func TestSubscriptions_SourcePriceDefaultOff(t *testing.T) {
	subs := newSubscriptions()
	if subs.wants("source_price", "") {
		t.Error("source_price should be off by default")
	}
}

func TestSubscriptions_SourceStatusDefaultOff(t *testing.T) {
	subs := newSubscriptions()
	if subs.wants("source_status", "") {
		t.Error("source_status should be off by default")
	}
}

func TestSubscriptions_SubscribeSourcePrice(t *testing.T) {
	subs := newSubscriptions()
	subs.set("source_price", true)
	if !subs.wants("source_price", "") {
		t.Error("source_price should be on after subscribe")
	}
}

func TestSubscriptions_SubscribeSourceStatus(t *testing.T) {
	subs := newSubscriptions()
	subs.set("source_status", true)
	if !subs.wants("source_status", "") {
		t.Error("source_status should be on after subscribe")
	}
}

func TestWSMessage_SourceFields(t *testing.T) {
	msg := WSMessage{
		Type:      "source_price",
		Source:    "binance",
		ConnState: "",
	}
	if msg.Source != "binance" {
		t.Errorf("expected source binance, got %s", msg.Source)
	}

	msg2 := WSMessage{
		Type:      "source_status",
		Source:    "kraken",
		ConnState: "disconnected",
		Stale:     true,
	}
	if msg2.ConnState != "disconnected" {
		t.Errorf("expected conn_state disconnected, got %s", msg2.ConnState)
	}
	if !msg2.Stale {
		t.Error("expected stale true")
	}
}
