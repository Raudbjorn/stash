package metadata

import (
	"regexp"
	"strconv"
	"time"
)

// dateFullRE matches YYYY-MM-DD style dates with '-', '.', '_', or space as the
// separator, and also the common 2-digit-year site convention (YY-MM-DD),
// assumed to be 20YY. It also matches 8 contiguous digits (YYYYMMDD).
var (
	dateFullSepRE = regexp.MustCompile(`\b(\d{2,4})[-._ ](\d{1,2})[-._ ](\d{1,2})\b`)
	// dateCompactRE deliberately doesn't use \b: underscore is a "word"
	// character in RE2, so "_20200501_" has no boundary there. Instead it
	// captures the (optional) non-digit context to ensure the 8 digits
	// aren't part of a longer digit run.
	dateCompactRE  = regexp.MustCompile(`(?:^|\D)(\d{4})(\d{2})(\d{2})(?:\D|$)`)
	dateYearOnlyRE = regexp.MustCompile(`\b(19|20)\d{2}\b`)
)

func normalizeYear(y int) int {
	if y < 100 {
		// 2-digit year convention used throughout this domain: 00-99 -> 2000-2099
		return 2000 + y
	}
	return y
}

func validDateParts(year, month, day int) bool {
	if year < 1900 || year > 2100 {
		return false
	}
	if month < 1 || month > 12 {
		return false
	}
	if day < 1 || day > 31 {
		return false
	}
	return true
}

// ExtractDateSignals scans free text (title, filename, etc.) for date-like
// substrings and returns them as DateSignals. Full dates (with a
// day-of-month) get DatePriorityTextFull; a bare 4-digit year with no
// accompanying full date gets DatePriorityTextYearOnly. source labels the
// signal's origin (e.g. "title", "filename") for logging/debugging.
func ExtractDateSignals(text string, source string) []DateSignal {
	var ret []DateSignal
	foundFull := false

	if m := dateFullSepRE.FindStringSubmatch(text); m != nil {
		year, _ := strconv.Atoi(m[1])
		month, _ := strconv.Atoi(m[2])
		day, _ := strconv.Atoi(m[3])
		year = normalizeYear(year)

		if validDateParts(year, month, day) {
			ret = append(ret, DateSignal{
				Date:     time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC),
				Priority: DatePriorityTextFull,
				Source:   source,
			})
			foundFull = true
		}
	}

	if !foundFull {
		if m := dateCompactRE.FindStringSubmatch(text); m != nil {
			year, _ := strconv.Atoi(m[1])
			month, _ := strconv.Atoi(m[2])
			day, _ := strconv.Atoi(m[3])

			if validDateParts(year, month, day) {
				ret = append(ret, DateSignal{
					Date:     time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC),
					Priority: DatePriorityTextFull,
					Source:   source,
				})
				foundFull = true
			}
		}
	}

	if !foundFull {
		if m := dateYearOnlyRE.FindString(text); m != "" {
			year, _ := strconv.Atoi(m)
			ret = append(ret, DateSignal{
				Date:     time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC),
				Priority: DatePriorityTextYearOnly,
				Source:   source,
			})
		}
	}

	return ret
}
