package dnscheck

import (
	"slices"
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

// TestVoteCases checks the named cases of the vote by hand. A resolver
// that fails (an error reply or no reply) is left out while another one
// answered: one failing resolver never makes the outcome broken.
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
		// No resolver replied at all: nothing is known (inconclusive).
		{[]answer{vN}, nil, none},
		{[]answer{vN, vN}, []answer{vD}, none},
		// Every resolver that replied replied with an error: DNS does not
		// answer normally for the name.
		{[]answer{vF}, nil, broken},
		{[]answer{vF, vN}, nil, broken},
		{[]answer{vF, vF}, []answer{vNX}, broken},
		{[]answer{vF}, []answer{vD}, broken},
		// Every resolver that answered: NXDOMAIN.
		{[]answer{vNX}, nil, notFound},
		{[]answer{vNX, vNX, vNX}, []answer{vNX, vN}, notFound},
		{[]answer{vNX}, []answer{vF}, notFound},
		{[]answer{vNX, vF}, nil, notFound}, // one failing resolver is left out
		{[]answer{vNX, vN, vNX}, []answer{vNX}, notFound},
		{[]answer{vNX}, []answer{vD}, broken},  // an authoritative server says it exists
		{[]answer{vNX}, []answer{vND}, broken}, // so does a NODATA of one
		// Every resolver that answered: an address to deliver to.
		{[]answer{vD}, nil, exists},
		{[]answer{vD, vD, vD}, []answer{vD, vD}, exists},
		{[]answer{vD, vF}, nil, exists}, // one failing resolver is left out
		{[]answer{vD, vN, vF}, nil, exists},
		{[]answer{vD}, []answer{vN, vN}, exists}, // unreachable authoritative servers do not vote
		{[]answer{vD}, []answer{vF}, exists},     // nor does a failing one
		{[]answer{vD}, []answer{vND}, exists},    // the name exists there
		{[]answer{vD}, []answer{vD, vNX}, broken},
		// The name exists without an address, or the answers disagree.
		{[]answer{vD, vNX}, nil, broken},
		{[]answer{vD, vND}, nil, broken},
		{[]answer{vND}, nil, broken},
		{[]answer{vND, vF}, nil, broken},
		{[]answer{vND, vND}, []answer{vND}, broken},
		{[]answer{vNX, vND}, nil, broken},
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

// referenceVote states the rules over what was seen: the answers of the
// resolvers that answered (a failure or no reply is left out; no reply from
// any resolver and no error reply either is inconclusive), and those of the
// authoritative servers that answered.
func referenceVote(resolvers, auth []answer) mailengine.DNSOutcome {
	answering := map[answer]bool{}
	for _, a := range resolvers {
		if a != vN && a != vF {
			answering[a] = true
		}
	}
	byAuth := map[answer]bool{}
	for _, a := range auth {
		if a != vN && a != vF {
			byAuth[a] = true
		}
	}
	only := func(set map[answer]bool, a answer) bool { return len(set) == 1 && set[a] }
	switch {
	case len(resolvers) == 0:
		return ""
	case len(answering) == 0 && !slices.Contains(resolvers, vF):
		return ""
	case len(answering) == 0:
		return mailengine.DNSOutcomeBroken
	case only(answering, vNX) && !byAuth[vD] && !byAuth[vND]:
		return mailengine.DNSOutcomeNotFound
	case only(answering, vD) && !byAuth[vNX]:
		return mailengine.DNSOutcomeExists
	}
	return mailengine.DNSOutcomeBroken
}

// TestTargetAndAddressAnswers checks how the answers of the lookups of one
// resolver combine (resolveName): an address of any MX target, or of
// either family, is data; otherwise no reply at all comes before an error
// reply (an address may be missing only because a server did not answer:
// inconclusive rather than broken), an error reply before no data, and a
// host is NXDOMAIN only when both families said so. A target that does not
// exist is one without an address.
func TestTargetAndAddressAnswers(t *testing.T) {
	for _, tc := range []struct {
		answers []answer
		want    answer
	}{
		{[]answer{vD}, vD},
		{[]answer{vN, vD}, vD},
		{[]answer{vF, vND, vD}, vD},
		{[]answer{vN, vND}, vN},
		{[]answer{vND, vN}, vN},
		{[]answer{vN, vF}, vN},
		{[]answer{vF, vN}, vN},
		{[]answer{vF, vND}, vF},
		{[]answer{vNX, vF}, vF},
		{[]answer{vNX, vND}, vND},
		{[]answer{vNX}, vND},
		{[]answer{vND, vND}, vND},
	} {
		if got := targetsAnswer(tc.answers); got != tc.want {
			t.Errorf("targetsAnswer(%s) = %s, want %s", answersText(tc.answers), answerNames[got], answerNames[tc.want])
		}
	}
	for _, tc := range []struct {
		answers []answer
		want    answer
	}{
		{[]answer{vD, vNX}, vD},
		{[]answer{vF, vD}, vD},
		{[]answer{vN, vD}, vD},
		{[]answer{vN, vF}, vN},
		{[]answer{vF, vN}, vN},
		{[]answer{vN, vNX}, vN},
		{[]answer{vF, vND}, vF},
		{[]answer{vNX, vF}, vF},
		{[]answer{vNX, vNX}, vNX},
		{[]answer{vNX, vND}, vND},
		{[]answer{vND, vNX}, vND},
		{[]answer{vND, vND}, vND},
	} {
		if got := addressesAnswer(tc.answers); got != tc.want {
			t.Errorf("addressesAnswer(%s) = %s, want %s", answersText(tc.answers), answerNames[got], answerNames[tc.want])
		}
	}
}
