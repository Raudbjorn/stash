// Package metadata provides scene metadata analysis: detecting performers
// and dates from scene titles, filenames, and other available signals.
package metadata

import "time"

// Signal priority tiers, lower is higher priority.
const (
	DatePriorityExif          = 1
	DatePriorityVideoCreation = 2
	DatePriorityTextFull      = 3
	DatePriorityTextYearOnly  = 4
)

// baseConfidence per priority tier.
var dateSignalBaseConfidence = map[int]float64{
	DatePriorityExif:          0.95,
	DatePriorityVideoCreation: 0.85,
	DatePriorityTextFull:      0.65,
	DatePriorityTextYearOnly:  0.4,
}

const (
	dateCorroborationBonus = 0.1
	dateMaxConfidence      = 0.99
	dateContestedCeiling   = 0.55
)

// DateSignal is a single candidate date, tagged with its source's priority
// tier (lower is higher priority - see the DatePriority* constants).
type DateSignal struct {
	Date     time.Time
	Priority int
	Source   string
}

// ResolvedDate is the outcome of resolving a set of DateSignals.
type ResolvedDate struct {
	Date       time.Time
	Confidence float64
	Source     string
	Contested  bool
}

// sameDate returns true if a and b fall on the same calendar day (UTC).
func sameDate(a, b time.Time) bool {
	ay, am, ad := a.UTC().Date()
	by, bm, bd := b.UTC().Date()
	return ay == by && am == bm && ad == bd
}

// agrees returns true if signal s corroborates the best signal's date. A
// year-only signal only needs to agree on the year; anything more precise
// must agree on the exact calendar day.
func agrees(best, s DateSignal) bool {
	if best.Priority == DatePriorityTextYearOnly || s.Priority == DatePriorityTextYearOnly {
		return best.Date.UTC().Year() == s.Date.UTC().Year()
	}
	return sameDate(best.Date, s.Date)
}

// ResolveDate picks the best date from the given signals using a
// priority-with-corroboration scheme. Signals are never averaged:
// conflicting dates would otherwise blend into a value nobody actually
// asserted. Any signal dated after sanityBound is discarded outright (e.g.
// a date can't be later than the file's own modification time). The
// highest-priority remaining signal wins; if a lower-priority signal
// independently agrees, confidence gets a small corroboration bonus; if
// signals disagree, the top signal is kept but flagged Contested with a
// capped confidence, signalling "log only, don't auto-apply".
//
// Returns nil if no signals survive the sanity bound.
func ResolveDate(signals []DateSignal, sanityBound time.Time) *ResolvedDate {
	var valid []DateSignal
	for _, s := range signals {
		if s.Date.After(sanityBound) {
			continue
		}
		valid = append(valid, s)
	}

	if len(valid) == 0 {
		return nil
	}

	best := valid[0]
	for _, s := range valid[1:] {
		if s.Priority < best.Priority {
			best = s
		}
	}

	confidence := dateSignalBaseConfidence[best.Priority]
	contested := false

	for _, s := range valid {
		if s.Priority == best.Priority && s.Source == best.Source {
			continue
		}

		if agrees(best, s) {
			confidence += dateCorroborationBonus
		} else {
			contested = true
		}
	}

	if contested {
		if confidence > dateContestedCeiling {
			confidence = dateContestedCeiling
		}
	} else if confidence > dateMaxConfidence {
		confidence = dateMaxConfidence
	}

	return &ResolvedDate{
		Date:       best.Date,
		Confidence: confidence,
		Source:     best.Source,
		Contested:  contested,
	}
}
