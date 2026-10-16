package mailengine

import (
	"math/big"
	"net/netip"
	"slices"
)

// SPFMaxSpaceBits4 and SPFMaxSpaceBits6 bound the address space that the
// prefixes the SPF records list may cover together and still count as
// sending sources (design document "mail classification" 2.7), every
// address counted once (overlapping and adjacent prefixes merged): 2^24
// IPv4 addresses (one /8) and 2^112 IPv6 addresses (one /16). Many narrower
// ranges would otherwise take every address for a sending source just as one range that
// wide would. The bound applies to the evaluation of each domain
// (app/modules/dnscheck) and to the prefixes of all the domains of a run
// together (sourceLookups); past it the SPF records do not count as read
// completely.
const (
	SPFMaxSpaceBits4 = 24
	SPFMaxSpaceBits6 = 112
)

// SPFTooMuchSpace reports whether prefixes cover more IPv4 addresses than
// SPFMaxSpaceBits4 or more IPv6 addresses than SPFMaxSpaceBits6 allow,
// every address counted once.
func SPFTooMuchSpace(prefixes []netip.Prefix) bool {
	one := big.NewInt(1)
	return AddressSpace(prefixes, true).Cmp(new(big.Int).Lsh(one, SPFMaxSpaceBits4)) > 0 ||
		AddressSpace(prefixes, false).Cmp(new(big.Int).Lsh(one, SPFMaxSpaceBits6)) > 0
}

// AddressSpace returns how many addresses of one family (IPv4 when is4) the
// prefixes cover together: the prefixes are taken as ranges, sorted, and
// overlapping or adjacent ranges merged before their sizes are added, so a
// range listed twice, or inside another, counts once.
func AddressSpace(prefixes []netip.Prefix, is4 bool) *big.Int {
	type span struct{ first, last *big.Int }
	one := big.NewInt(1)
	var spans []span
	for _, p := range prefixes {
		if !p.IsValid() || p.Addr().Is4() != is4 {
			continue
		}
		p = p.Masked()
		first := new(big.Int).SetBytes(p.Addr().AsSlice())
		size := new(big.Int).Lsh(one, uint(p.Addr().BitLen()-p.Bits()))
		spans = append(spans, span{first: first, last: new(big.Int).Sub(new(big.Int).Add(first, size), one)})
	}
	slices.SortFunc(spans, func(a, b span) int { return a.first.Cmp(b.first) })
	total := new(big.Int)
	var cur *span
	for i := range spans {
		s := &spans[i]
		if cur != nil && s.first.Cmp(new(big.Int).Add(cur.last, one)) <= 0 {
			if s.last.Cmp(cur.last) > 0 {
				cur.last = s.last
			}
			continue
		}
		if cur != nil {
			total.Add(total, new(big.Int).Sub(cur.last, cur.first)).Add(total, one)
		}
		cur = s
	}
	if cur != nil {
		total.Add(total, new(big.Int).Sub(cur.last, cur.first)).Add(total, one)
	}
	return total
}
