package store

import (
	"database/sql"
	"database/sql/driver"
	"fmt"
	"time"
)

// Timestamps are stored as INTEGER unix milliseconds, in UTC.
//
// The Postgres original used `timestamp` columns with CURRENT_TIMESTAMP
// defaults. Reproducing that on SQLite would mean second-resolution text, which
// the interactions pipeline cannot use: it compares event times against merge
// windows measured in seconds and margins measured in fractions of one. Integers
// are unambiguous about timezone, index cleanly, and compare correctly.

// NowMillis is the current time in unix milliseconds. Every timestamp column is
// written explicitly through this rather than by a column default.
func NowMillis() int64 { return time.Now().UTC().UnixMilli() }

// ToMillis converts a time to the stored representation.
func ToMillis(t time.Time) int64 { return t.UTC().UnixMilli() }

// FromMillis converts a stored value back to a UTC time.
func FromMillis(ms int64) time.Time { return time.UnixMilli(ms).UTC() }

// NullTime adapts a nullable timestamp column to a *time.Time.
//
// It reads INTEGER milliseconds, and additionally tolerates the TEXT forms a
// legacy SQLite database might hold, so importing an older file does not fail
// with a scan error.
type NullTime struct {
	Time  time.Time
	Valid bool
}

// Scan implements sql.Scanner.
func (n *NullTime) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		n.Time, n.Valid = time.Time{}, false
		return nil
	case int64:
		n.Time, n.Valid = FromMillis(v), true
		return nil
	case float64:
		n.Time, n.Valid = FromMillis(int64(v)), true
		return nil
	case time.Time:
		n.Time, n.Valid = v.UTC(), true
		return nil
	case []byte:
		return n.scanString(string(v))
	case string:
		return n.scanString(v)
	default:
		return fmt.Errorf("store: cannot scan %T into NullTime", src)
	}
}

func (n *NullTime) scanString(s string) error {
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			n.Time, n.Valid = t.UTC(), true
			return nil
		}
	}
	return fmt.Errorf("store: cannot parse %q as a timestamp", s)
}

// Value implements driver.Valuer.
func (n NullTime) Value() (driver.Value, error) {
	if !n.Valid {
		return nil, nil
	}
	return ToMillis(n.Time), nil
}

// TimePtr returns the time as a pointer, nil when not valid.
func (n NullTime) TimePtr() *time.Time {
	if !n.Valid {
		return nil
	}
	t := n.Time
	return &t
}

// NullTimeFrom builds a NullTime from an optional time.
func NullTimeFrom(t *time.Time) NullTime {
	if t == nil {
		return NullTime{}
	}
	return NullTime{Time: t.UTC(), Valid: true}
}

// MillisPtr renders an optional time as the stored representation, for use as a
// query argument.
func MillisPtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return ToMillis(*t)
}

// NullString is a small convenience over sql.NullString for optional text.
func NullString(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

// StringPtr converts a scanned sql.NullString back to an optional string.
func StringPtr(n sql.NullString) *string {
	if !n.Valid {
		return nil
	}
	v := n.String
	return &v
}

// Float64Ptr converts a scanned sql.NullFloat64 back to an optional float.
func Float64Ptr(n sql.NullFloat64) *float64 {
	if !n.Valid {
		return nil
	}
	v := n.Float64
	return &v
}

// Int64Ptr converts a scanned sql.NullInt64 back to an optional int64.
func Int64Ptr(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	v := n.Int64
	return &v
}
