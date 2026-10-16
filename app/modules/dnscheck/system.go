package dnscheck

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"time"
)

// The OS resolver (Config.SystemOnly), asked through Checker.sys
// (net.DefaultResolver). Go reports NXDOMAIN and an empty answer alike as
// "not found" (net.DNSError.IsNotFound), so here both count as NXDOMAIN: a
// name that exists without the records asked for ends as not_found where
// the configured resolvers would give broken. For A and AAAA (and the
// implicit MX), an address of either family shows that the name exists, so
// the name is asked for both families before it counts as not found. Any
// other error counts as a failure. No authoritative server is asked
// directly in this mode: only the OS resolver is ever contacted.
//
// Every name is asked as an absolute name (with the trailing dot), so the
// resolver never tries it under the search domains of the host: a wildcard
// record of a search domain would turn a name that does not exist into one
// that does. Only the single-label host names of the settings are asked as
// written (lookupSourceSystem). The source host names are asked of the OS
// resolver first whatever the mode (Checker.ResolveHost).

// defaultSystemResolver returns the OS resolver of a new Checker or
// SystemHostResolver (net.DefaultResolver). The tests replace it, so that
// none of them asks the resolver of the host by accident.
var defaultSystemResolver = func() systemResolver { return net.DefaultResolver }

// systemResolver is what the Checker asks of the OS resolver
// (*net.Resolver; tests wrap it).
type systemResolver interface {
	LookupHost(ctx context.Context, host string) ([]string, error)
	LookupMX(ctx context.Context, name string) ([]*net.MX, error)
	LookupNS(ctx context.Context, name string) ([]*net.NS, error)
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
	LookupTXT(ctx context.Context, name string) ([]string, error)
}

// absolute returns a normalized name as an absolute name (trailing dot).
func absolute(name string) string {
	return name + "."
}

// systemControl runs the control query through the OS resolver.
func (c *Checker) systemControl(ctx context.Context) (bool, string) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	addrs, err := c.sys.LookupHost(ctx, absolute(controlName))
	if err != nil {
		_, note := systemError(err)
		return false, note
	}
	if len(addrs) == 0 {
		return false, "no data"
	}
	return true, "ok"
}

// systemLookup asks the OS resolver the question of a verification. For A
// and AAAA, a name without addresses of the family asked (reported as not
// found) is asked for addresses of either family: one shows that the name
// exists. For MX, a name without MX records (reported as not found) is
// asked for its addresses (the implicit MX).
func (c *Checker) systemLookup(ctx context.Context, name, qtype string) (answer, string) {
	switch qtype {
	case "A", "AAAA":
		network := "ip4"
		if qtype == "AAAA" {
			network = "ip6"
		}
		_, a, note := c.systemIP(ctx, network, name)
		if a != answerNX {
			return a, qtype + " " + note
		}
		if _, other, otherNote := c.systemIP(ctx, "ip", name); other == answerData {
			return answerData, qtype + " " + note + "; address " + otherNote + " (no " + qtype + " record, name exists)"
		}
		return a, qtype + " " + note
	}
	_, a, note := c.systemMXNote(ctx, name)
	if a != answerNX && a != answerNoData {
		return a, "MX " + note
	}
	_, got, addrNote := c.systemIP(ctx, "ip", name)
	return got, "MX " + note + "; address " + addrNote
}

// systemMX returns the MX target names of a domain through the OS
// resolver (for the applicability of a name).
func (c *Checker) systemMX(ctx context.Context, domain string) ([]string, answer) {
	hosts, a, _ := c.systemMXNote(ctx, domain)
	return hosts, a
}

func (c *Checker) systemMXNote(ctx context.Context, domain string) ([]string, answer, string) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	mxs, err := c.sys.LookupMX(ctx, absolute(domain))
	if err != nil {
		a, note := systemError(err)
		return nil, a, note
	}
	var hosts []string
	for _, mx := range mxs {
		hosts = append(hosts, hostText(mx.Host))
	}
	if len(hosts) == 0 {
		return nil, answerNoData, "no data"
	}
	return hosts, answerData, "ok (" + quote(hosts) + ")"
}

// systemRecords returns the records of one type of a name through the OS
// resolver, asked as an absolute name (the SPF evaluation, spf.go): the
// text of the TXT records (Go joins the character-strings of a record), the
// MX targets, or the A or AAAA addresses.
func (c *Checker) systemRecords(ctx context.Context, name string, t queryType) ([]string, answer) {
	switch t {
	case typeMX:
		return c.systemMX(ctx, name)
	case typeA, typeAAAA:
		network := "ip4"
		if t == typeAAAA {
			network = "ip6"
		}
		addrs, a, _ := c.systemIP(ctx, network, name)
		values := make([]string, 0, len(addrs))
		for _, addr := range addrs {
			values = append(values, addr.String())
		}
		return values, a
	}
	lookupCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	txts, err := c.sys.LookupTXT(lookupCtx, absolute(name))
	if err != nil {
		a, _ := systemError(err)
		return nil, a
	}
	if len(txts) == 0 {
		return nil, answerNoData
	}
	return txts, answerData
}

// systemNS returns the name servers of a domain through the OS resolver.
func (c *Checker) systemNS(ctx context.Context, domain string) ([]string, answer) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	nss, err := c.sys.LookupNS(ctx, absolute(domain))
	if err != nil {
		a, _ := systemError(err)
		return nil, a
	}
	var hosts []string
	for _, ns := range nss {
		hosts = append(hosts, hostText(ns.Host))
	}
	if len(hosts) == 0 {
		return nil, answerNoData
	}
	return hosts, answerData
}

// systemAddresses returns the A and AAAA addresses of a host through the
// OS resolver.
func (c *Checker) systemAddresses(ctx context.Context, host string) ([]netip.Addr, answer) {
	addrs, a, _ := c.systemIP(ctx, "ip", host)
	return addrs, a
}

// lookupSourceSystem looks up the A and AAAA addresses of one of the source host
// names through an OS resolver, the name passed as it is (absolute, or a
// single-label name of the settings as written so that the search list and
// the hosts file apply), and reports whether both families were answered
// (addresses, or not found). Each family is asked separately, within
// timeout each: asked for both at once, Go returns the addresses of one
// family without an error when the lookup of the other failed, and half an
// answer must not pass for a whole one.
func lookupSourceSystem(ctx context.Context, sys systemResolver, timeout time.Duration, host string) ([]netip.Addr, bool) {
	var addrs []netip.Addr
	answered := true
	for _, network := range []string{"ip4", "ip6"} {
		got, a, _ := lookupSystemIP(ctx, sys, timeout, network, host)
		addrs = append(addrs, got...)
		if a == answerFail || a == answerNone {
			answered = false
		}
	}
	return addrs, answered
}

// systemIP looks up the addresses of a host (network "ip", "ip4" or "ip6")
// as an absolute name.
func (c *Checker) systemIP(ctx context.Context, network, host string) ([]netip.Addr, answer, string) {
	return c.systemIPAs(ctx, network, absolute(host))
}

// systemIPAs is systemIP with the name passed to the resolver as it is.
func (c *Checker) systemIPAs(ctx context.Context, network, host string) ([]netip.Addr, answer, string) {
	return lookupSystemIP(ctx, c.sys, c.timeout, network, host)
}

// lookupSystemIP looks up the addresses of a host (network "ip", "ip4" or
// "ip6") through an OS resolver, the name passed as it is, within timeout.
// The addresses are unmapped.
func lookupSystemIP(ctx context.Context, sys systemResolver, timeout time.Duration, network, host string) ([]netip.Addr, answer, string) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	addrs, err := sys.LookupNetIP(ctx, network, host)
	if err != nil {
		a, note := systemError(err)
		return nil, a, note
	}
	values := make([]string, 0, len(addrs))
	for i, a := range addrs {
		addrs[i] = a.Unmap()
		values = append(values, addrs[i].String())
	}
	if len(addrs) == 0 {
		return nil, answerNoData, "no data"
	}
	return addrs, answerData, "ok (" + quote(values) + ")"
}

// systemError classifies an error of the OS resolver: not found (NXDOMAIN
// or no data, which Go does not tell apart) counts as NXDOMAIN, and so does
// a name found without an address of the family asked (Go reports a name of
// the hosts file that has only addresses of the other family so); anything
// else counts as a failure.
func systemError(err error) (answer, string) {
	var de *net.DNSError
	if errors.As(err, &de) {
		switch {
		case de.IsNotFound:
			return answerNX, "not found (NXDOMAIN or no data)"
		case de.IsTimeout:
			return answerFail, "timeout"
		}
		return answerFail, de.Err
	}
	var ae *net.AddrError
	if errors.As(err, &ae) {
		return answerNX, "not found (no address of the family)"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return answerFail, "timeout"
	}
	return answerFail, "error"
}
