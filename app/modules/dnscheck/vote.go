package dnscheck

import "mailcare/app/modules/mailengine"

// answer is what one resolver or authoritative server said about a name.
type answer int

const (
	// answerNone is no usable reply: no response at all (after the
	// retransmission), or a reply of an authoritative server that is not
	// authoritative for the name. It shows nothing about the name: a
	// resolver that gave it does not vote (when none gave anything else,
	// the name is inconclusive), and neither does such an authoritative
	// server.
	answerNone answer = iota
	// answerData: from a resolver verifying a name (resolveName), an
	// address to deliver to was obtained; from an authoritative server, or
	// a single question, records of the type asked for.
	answerData
	answerNX // NXDOMAIN
	// answerNoData: the name exists without the records asked for; from a
	// resolver verifying a name, it exists without an address to deliver
	// to (no MX target has one, no address without MX, or a null MX).
	answerNoData
	answerFail // an error reply: SERVFAIL, REFUSED, ...
)

// vote decides the outcome of a verification (design document "mail
// classification" 4.4) from the answers of the reachable resolvers (those
// that passed their control query; each verified the whole name,
// resolveName) and of the authoritative servers (whether the name exists).
// A resolver or authoritative server that failed (an error reply or no
// reply) is left out as long as another one answered: one failing server
// says nothing about the name. No reply at all is no answer: it shows
// nothing about the name either. The rules are evaluated in this order and
// the first that holds wins:
//
//   - not verified: no reachable resolver, or no resolver replied at all
//     (every query timed out: inconclusive, verified again later);
//   - broken: the resolvers that replied all replied with an error
//     (SERVFAIL, REFUSED: DNS does not answer normally for the name);
//   - not_found: every resolver that answered returned NXDOMAIN and no
//     authoritative server says the name exists;
//   - exists: every resolver that answered obtained an address and no
//     authoritative server returned NXDOMAIN;
//   - broken: anything else: the name exists without an address to
//     deliver to (no data), or the answers disagree.
func vote(resolvers, authoritative []answer) (mailengine.DNSOutcome, bool) {
	if len(resolvers) == 0 {
		return "", false
	}
	var r [answerFail + 1]int
	for _, x := range resolvers {
		r[x]++
	}
	authNX, authExists := 0, 0
	for _, x := range authoritative {
		switch x {
		case answerNX:
			authNX++
		case answerData, answerNoData:
			authExists++
		}
	}
	answering := r[answerData] + r[answerNX] + r[answerNoData]
	switch {
	case answering == 0 && r[answerFail] == 0:
		return "", false
	case answering == 0:
		return mailengine.DNSOutcomeBroken, true
	case r[answerNX] == answering && authExists == 0:
		return mailengine.DNSOutcomeNotFound, true
	case r[answerData] == answering && authNX == 0:
		return mailengine.DNSOutcomeExists, true
	}
	return mailengine.DNSOutcomeBroken, true
}
