/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 3.2.0
 */

// Package archivezone validates the narrow timezone constraint required by
// archive's UTC-hour summary buckets.
package archivezone

import (
	"fmt"
	"strconv"
	"time"
)

// Validate loads timezone and verifies all offsets spanning the previous,
// current, and next calendar years align to complete UTC hours.
func Validate(timezone string) (*time.Location, error) {
	return ValidateAt(timezone, time.Now())
}

// ValidateAt applies the seasonal offset check around now. It exists so a
// sweep can validate the time it is about to archive rather than process state.
func ValidateAt(timezone string, now time.Time) (*time.Location, error) {
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, fmt.Errorf("load timezone %q: %w", timezone, err)
	}
	localNow := now.In(loc)
	start := time.Date(localNow.Year()-1, time.January, 1, 0, 0, 0, 0, loc).UTC()
	end := time.Date(localNow.Year()+2, time.January, 1, 0, 0, 0, 0, loc).UTC()
	if err := validateOffsets(loc, start, end); err != nil {
		return nil, err
	}
	return loc, nil
}

// ValidateInterval rejects a local archive day whose UTC boundaries or zone
// offsets would split one of archive's complete UTC-hour buckets.
func ValidateInterval(loc *time.Location, start, end time.Time) error {
	if !start.Equal(start.Truncate(time.Hour)) || !end.Equal(end.Truncate(time.Hour)) {
		return fmt.Errorf("archive timezone %q has local-day boundaries outside complete UTC hours", loc)
	}
	return validateOffsets(loc, start, end)
}

func validateOffsets(loc *time.Location, start, end time.Time) error {
	for at := start; at.Before(end); {
		local := at.In(loc)
		name, offset := local.Zone()
		if offset%int(time.Hour/time.Second) != 0 {
			return fmt.Errorf("archive timezone %q has fractional-hour UTC offset %s (%s); use a whole-hour timezone or disable archive.enabled", loc, name, formatOffset(offset))
		}
		_, zoneEnd := local.ZoneBounds()
		if zoneEnd.IsZero() || !zoneEnd.Before(end) {
			break
		}
		if !zoneEnd.After(at) {
			return fmt.Errorf("archive timezone %q has an invalid zone interval", loc)
		}
		at = zoneEnd
	}
	return nil
}

func formatOffset(offset int) string {
	sign := "+"
	if offset < 0 {
		sign = "-"
		offset = -offset
	}
	return sign + strconv.Itoa(offset/3600) + ":" + fmt.Sprintf("%02d", (offset%3600)/60)
}
