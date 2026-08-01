package util

import (
	"context"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/store"
)

// FallbackTimezone is the last-resort timezone used when no organization
// timezone is configured in config_store (bot/calendar_timezone).
const FallbackTimezone = "America/Chicago"

// FallbackLocation returns the time.Location for FallbackTimezone.
func FallbackLocation() *time.Location {
	loc, _ := time.LoadLocation(FallbackTimezone)
	return loc
}

// DefaultTimezone returns the organization's configured timezone
// (config_store bot/calendar_timezone), falling back to FallbackTimezone when
// unset, unreadable, or not a valid IANA timezone name.
func DefaultTimezone(ctx context.Context, pool *pgxpool.Pool, encKey []byte) string {
	tz, err := store.GetServiceConfig(ctx, pool, "bot", "calendar_timezone", encKey)
	if err != nil || tz == "" {
		if err != nil {
			log.Printf("util: error reading calendar_timezone config, defaulting to %s: %v", FallbackTimezone, err)
		}
		return FallbackTimezone
	}
	if _, lerr := time.LoadLocation(tz); lerr != nil {
		log.Printf("util: invalid timezone %q configured, defaulting to %s", tz, FallbackTimezone)
		return FallbackTimezone
	}
	return tz
}

// OrgLocation returns the time.Location for the organization's configured
// timezone (see DefaultTimezone).
func OrgLocation(ctx context.Context, pool *pgxpool.Pool, encKey []byte) *time.Location {
	loc, err := time.LoadLocation(DefaultTimezone(ctx, pool, encKey))
	if err != nil {
		return FallbackLocation()
	}
	return loc
}

// LocalizeMeetingDate converts a UTC meeting timestamp to the organization's local timezone.
func LocalizeMeetingDate(ctx context.Context, pool *pgxpool.Pool, encKey []byte, utcTime time.Time) time.Time {
	return utcTime.In(OrgLocation(ctx, pool, encKey))
}
