/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 3.2.0
 */

package archivezone

import (
	"testing"
	"time"
)

func TestValidateAtRejectsSeasonalFractionalOffsets(t *testing.T) {
	winterLordHowe := time.Date(2026, time.January, 15, 12, 0, 0, 0, time.UTC)
	for _, timezone := range []string{"Asia/Kolkata", "Asia/Kathmandu", "Australia/Lord_Howe"} {
		if _, err := ValidateAt(timezone, winterLordHowe); err == nil {
			t.Errorf("ValidateAt(%q) unexpectedly succeeded", timezone)
		}
	}
}

func TestValidateAtAllowsWholeHourOffsets(t *testing.T) {
	for _, timezone := range []string{"Asia/Shanghai", "UTC", "America/New_York"} {
		if _, err := ValidateAt(timezone, time.Date(2026, time.January, 15, 12, 0, 0, 0, time.UTC)); err != nil {
			t.Errorf("ValidateAt(%q): %v", timezone, err)
		}
	}
}
