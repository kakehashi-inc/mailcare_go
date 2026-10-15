package dnscheck

import (
	"testing"

	"mailcare/app/modules/mailengine"
)

// Short names of the answers for the tables.
const (
	vD  = answerData
	vNX = answerNX
	vND = answerNoData
	vF  = answerFail
	vN  = answerNone
)

var answerNames = map[answer]string{vD: "data", vNX: "nx", vND: "nodata", vF: "fail", vN: "none"}

func answersText(as []answer) string {
	s := "["
	for i, a := range as {
		if i > 0 {
			s += " "
		}
		s += answerNames[a]
	}
	return s + "]"
}

// TestVoteCases checks the named cases of the vote by hand.
func TestVoteCases(t *testing.T) {
	const (
		exists   = mailengine.DNSOutcomeExists
		notFound = mailengine.DNSOutcomeNotFound
		broken   = mailengine.DNSOutcomeBroken
		none     = mailengine.DNSOutcome("")
	)
	for _, tc := range []struct {
		resolvers, auth []answer
		want            mailengine.DNSOutcome
	}{
		// No reachable resolver: not verified, whatever the authoritative servers say.
		{nil, nil, none},
		{nil, []answer{vD}, none},
		{nil, []answer{vNX}, none},
		// Every resolver failed (an error reply or no reply).
		{[]answer{vF}, nil, none},
		{[]answer{vF, vN}, nil, none},
		{[]answer{vF, vF}, []answer{vNX}, none},
		{[]answer{vF}, []answer{vND}, none}, // the inconclusive rule comes first
		{[]answer{vF}, []answer{vF}, none},
		{[]answer{vF}, []answer{vD}, broken}, // an authoritative answer under failing resolvers
		{[]answer{vN, vF}, []answer{vN, vD}, broken},
		// Every resolver NXDOMAIN.
		{[]answer{vNX}, nil, notFound},
		{[]answer{vNX, vNX, vNX}, []answer{vNX, vN}, notFound},
		{[]answer{vNX}, []answer{vF}, notFound},
		{[]answer{vNX}, []answer{vND}, notFound}, // the not_found rule comes before broken
		{[]answer{vNX, vNX}, []answer{vD}, broken},
		// Every resolver data.
		{[]answer{vD}, nil, exists},
		{[]answer{vD, vD, vD}, []answer{vD, vD}, exists},
		{[]answer{vD}, []answer{vN, vN}, exists}, // unreachable authoritative servers do not vote
		{[]answer{vD}, []answer{vD, vNX}, broken},
		{[]answer{vD}, []answer{vND}, broken},
		{[]answer{vD}, []answer{vF}, broken},
		// Mixed resolvers.
		{[]answer{vD, vNX}, nil, broken},
		{[]answer{vD, vF}, nil, broken},
		{[]answer{vD, vN}, nil, broken},
		{[]answer{vND}, nil, broken},
		{[]answer{vND, vND}, []answer{vND}, broken},
		{[]answer{vNX, vND}, nil, broken},
		{[]answer{vND, vF}, nil, broken},
		// NXDOMAIN mixed with failures only: no rule holds.
		{[]answer{vNX, vF}, nil, none},
		{[]answer{vNX, vN}, []answer{vNX}, none},
		{[]answer{vNX, vF}, []answer{vF}, none},
	} {
		got, ok := vote(tc.resolvers, tc.auth)
		if got != tc.want || ok != (tc.want != "") {
			t.Errorf("vote(%s, %s) = %q, %v; want %q", answersText(tc.resolvers), answersText(tc.auth), got, ok, tc.want)
		}
	}
}

// TestVoteExhaustive runs every combination of up to three resolvers and up
// to two authoritative servers against the rules of the design document,
// restated here over the sets of answers given.
func TestVoteExhaustive(t *testing.T) {
	all := []answer{vD, vNX, vND, vF, vN}
	var lists func(n int) [][]answer
	lists = func(n int) [][]answer {
		if n == 0 {
			return [][]answer{nil}
		}
		var out [][]answer
		for _, l := range lists(n - 1) {
			for _, a := range all {
				out = append(out, append(append([]answer(nil), l...), a))
			}
		}
		return out
	}
	count := 0
	for nr := 0; nr <= 3; nr++ {
		for na := 0; na <= 2; na++ {
			for _, rs := range lists(nr) {
				for _, as := range lists(na) {
					count++
					want := referenceVote(rs, as)
					got, ok := vote(rs, as)
					if got != want || ok != (want != "") {
						t.Errorf("vote(%s, %s) = %q, %v; want %q", answersText(rs), answersText(as), got, ok, want)
					}
				}
			}
		}
	}
	if want := (1 + 5 + 25 + 125) * (1 + 5 + 25); count != want {
		t.Fatalf("%d combinations, want %d", count, want)
	}
}

// referenceVote states the rules over what was seen: byResolver the answers of
// the resolvers (no reply is a failure), byAuth those of the authoritative servers
// that were reached.
func referenceVote(resolvers, auth []answer) mailengine.DNSOutcome {
	byResolver := map[answer]bool{}
	for _, a := range resolvers {
		if a == vN {
			a = vF
		}
		byResolver[a] = true
	}
	byAuth := map[answer]bool{}
	for _, a := range auth {
		if a != vN {
			byAuth[a] = true
		}
	}
	only := func(set map[answer]bool, a answer) bool { return len(set) == 1 && set[a] }
	switch {
	case len(resolvers) == 0:
		return ""
	case only(byResolver, vF) && !byAuth[vD]:
		return ""
	case only(byResolver, vNX) && !byAuth[vD]:
		return mailengine.DNSOutcomeNotFound
	case only(byResolver, vD) && !byAuth[vNX] && !byAuth[vND] && !byAuth[vF]:
		return mailengine.DNSOutcomeExists
	case byResolver[vD] || byAuth[vD] || byResolver[vND] || byAuth[vND]:
		return mailengine.DNSOutcomeBroken
	}
	return ""
}
