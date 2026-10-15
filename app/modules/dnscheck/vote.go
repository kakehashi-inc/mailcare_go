package dnscheck

import "mailcare/app/modules/mailengine"

// answer is what one resolver or authoritative server said about a name.
type answer int

const (
	// answerNone is no usable reply: no response at all, or a reply of an
	// authoritative server that is not authoritative for the name. From a
	// resolver it counts as a failure; an authoritative server that gave it
	// was not reached and does not vote.
	answerNone   answer = iota
	answerData          // records of the type asked for (for MX, also an address of the implicit MX)
	answerNX            // NXDOMAIN
	answerNoData        // the name exists without such records
	answerFail          // an error reply: SERVFAIL, REFUSED, ...
)

// vote decides the outcome of a verification (design document "mail
// classification" 4.4) from the answers of the reachable resolvers (those
// that passed their control query) and of the authoritative servers. The
// rules are evaluated in this order and the first that holds wins:
//
//   - not verified: no reachable resolver, or every reachable resolver
//     failed (an error reply or no reply) and no authoritative server
//     returned data;
//   - not_found: every reachable resolver returned NXDOMAIN and no
//     authoritative server returned data;
//   - exists: every reachable resolver returned data and no authoritative
//     server that was reached returned NXDOMAIN, no data or an error;
//   - broken: any resolver or authoritative server returned data, or any
//     returned no data (the name exists, but its mail records or its DNS
//     are not healthy). NXDOMAIN mixed with data is broken;
//   - not verified: anything else (NXDOMAIN mixed with failures only).
func vote(resolvers, authoritative []answer) (mailengine.DNSOutcome, bool) {
	if len(resolvers) == 0 {
		return "", false
	}
	var r, a [answerFail + 1]int
	for _, x := range resolvers {
		if x == answerNone {
			x = answerFail
		}
		r[x]++
	}
	for _, x := range authoritative {
		a[x]++ // answerNone: not reached, ignored below
	}
	n := len(resolvers)
	switch {
	case r[answerFail] == n && a[answerData] == 0:
		return "", false
	case r[answerNX] == n && a[answerData] == 0:
		return mailengine.DNSOutcomeNotFound, true
	case r[answerData] == n && a[answerNX]+a[answerNoData]+a[answerFail] == 0:
		return mailengine.DNSOutcomeExists, true
	case r[answerData]+a[answerData] > 0 || r[answerNoData]+a[answerNoData] > 0:
		return mailengine.DNSOutcomeBroken, true
	}
	return "", false
}
