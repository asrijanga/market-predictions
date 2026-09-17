package quant

import (
	"fmt"
	"time"
)

// ThirdFriday returns the standard monthly options expiration for the
// given month.
func ThirdFriday(year int, month time.Month) time.Time {
	first := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
	offset := (int(time.Friday) - int(first.Weekday()) + 7) % 7
	return first.AddDate(0, 0, offset+14)
}

// TradingDays counts weekdays after from, up to and including to. Exchange
// holidays are not excluded; the small overcount is immaterial for
// multi-month horizons.
func TradingDays(from, to time.Time) int {
	from = time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, time.UTC)
	to = time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, time.UTC)
	if !to.After(from) {
		return 0
	}
	n := 0
	for d := from.AddDate(0, 0, 1); !d.After(to); d = d.AddDate(0, 0, 1) {
		if wd := d.Weekday(); wd != time.Saturday && wd != time.Sunday {
			n++
		}
	}
	return n
}

// ParseExpiry accepts "YYYY-MM" (resolved to that month's third Friday) or
// an explicit "YYYY-MM-DD" date.
func ParseExpiry(s string) (time.Time, error) {
	if t, err := time.Parse("2006-01", s); err == nil {
		return ThirdFriday(t.Year(), t.Month()), nil
	}
	if t, err := time.Parse(time.DateOnly, s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("expiry %q: want YYYY-MM or YYYY-MM-DD", s)
}
