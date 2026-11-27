package peers

import (
	"time"
)

// thresholds of the choice between the original and the compressed copy of a stream.
const (
	// frames in error, between two checks, beyond which the link is considered in trouble.
	errorsPerCheck = 3
	// share of packets lost on the way, between two checks, beyond which the link is considered
	// in trouble. Protocols like SRT recover lost packets by sending them again, which hides
	// the loss from viewers but takes bandwidth that a constrained link does not have.
	lossRatioLimit = 0.10
	// consecutive checks in trouble before giving up on the original.
	troubledChecks = 2
	// time without a stream before giving up on the original.
	unavailableLimit = 15 * time.Second
	// time spent on the compressed copy before trying the original again.
	// It doubles after each attempt that fails, up to the maximum.
	initialHold = 2 * time.Minute
	maxHold     = 30 * time.Minute
	// time the original must survive, after an attempt, for the attempt to be a success.
	probation = time.Minute
)

// Health is what is observed about a pulled stream between two checks.
type Health struct {
	Available bool
	// total number of frames in error since the stream started.
	FramesInError uint64
	// total number of packets received, and of packets that were lost on the way
	// (whether they were recovered later or not), since the connection started.
	PacketsReceived uint64
	PacketsLost     uint64
}

// Link decides what to pull from a peer for a stream in "auto" mode: the original
// as long as the link carries it cleanly, the compressed copy otherwise.
type Link struct {
	// what is being pulled: "original" or "compressed".
	Variant string

	lastErrors       uint64
	lastReceived     uint64
	lastLost         uint64
	hasLastErrors    bool
	troubled         int
	unavailableSince time.Time
	hold             time.Duration
	holdUntil        time.Time
	probationUntil   time.Time
}

// Update takes the health of the stream and returns what should be pulled from now on.
func (l *Link) Update(h Health, now time.Time) string {
	if l.hold == 0 {
		l.hold = initialHold
	}

	// frames in error since the last check. The counter restarts when the stream does.
	var delta uint64
	if l.hasLastErrors && h.FramesInError >= l.lastErrors {
		delta = h.FramesInError - l.lastErrors
	}
	// share of packets lost since the last check. Counters restart when the connection does.
	lossRatio := 0.0
	if l.hasLastErrors && h.PacketsReceived >= l.lastReceived && h.PacketsLost >= l.lastLost {
		received := h.PacketsReceived - l.lastReceived
		lost := h.PacketsLost - l.lastLost
		if received+lost >= 50 {
			lossRatio = float64(lost) / float64(received+lost)
		}
	}

	l.lastErrors = h.FramesInError
	l.lastReceived = h.PacketsReceived
	l.lastLost = h.PacketsLost
	l.hasLastErrors = true

	if h.Available {
		l.unavailableSince = time.Time{}
	} else if l.unavailableSince.IsZero() {
		l.unavailableSince = now
	}

	if l.Variant == "compressed" {
		// time to try the original again
		if now.After(l.holdUntil) {
			l.switchTo("original")
			l.probationUntil = now.Add(probation)
		}
		return l.Variant
	}

	if delta >= errorsPerCheck || lossRatio >= lossRatioLimit {
		l.troubled++
	} else {
		l.troubled = 0
	}

	inTrouble := l.troubled >= troubledChecks ||
		(!l.unavailableSince.IsZero() && now.Sub(l.unavailableSince) >= unavailableLimit)

	if inTrouble {
		// an attempt to go back to the original that fails makes the next one wait longer
		if now.Before(l.probationUntil) {
			l.hold = min(l.hold*2, maxHold)
		}
		l.switchTo("compressed")
		l.holdUntil = now.Add(l.hold)
		l.probationUntil = time.Time{}
		return l.Variant
	}

	// the original has held: next time, the wait starts from the beginning
	if !l.probationUntil.IsZero() && now.After(l.probationUntil) {
		l.probationUntil = time.Time{}
		l.hold = initialHold
	}

	return l.Variant
}

func (l *Link) switchTo(variant string) {
	l.Variant = variant
	l.troubled = 0
	l.hasLastErrors = false
	l.unavailableSince = time.Time{}
}
