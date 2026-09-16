package main

import (
	"testing"
	"time"
)

func TestResolveHorizonFromExpiry(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	exp, days, err := resolveHorizon(config{expiry: "2026-11"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if exp.Format(time.DateOnly) != "2026-11-20" || days != 47 {
		t.Fatalf("expiry=%s days=%d", exp.Format(time.DateOnly), days)
	}
}

func TestResolveHorizonRejectsNearExpiry(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	if _, _, err := resolveHorizon(config{expiry: "2026-09-18"}, now); err == nil {
		t.Fatal("expected error for expiry two days out")
	}
}

func TestResolveHorizonDefault(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	exp, days, err := resolveHorizon(config{horizon: 63}, now)
	if err != nil {
		t.Fatal(err)
	}
	if days != 63 || exp.Weekday() != time.Friday || exp.Month() != time.December {
		t.Fatalf("expiry=%s days=%d", exp.Format(time.DateOnly), days)
	}
}
