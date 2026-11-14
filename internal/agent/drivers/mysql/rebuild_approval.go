package mysql

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"net"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// RebuildRequestFile and RebuildApprovalFile sit in the pin directory.
	RebuildRequestFile  = "rebuild-request"
	RebuildApprovalFile = "rebuild-approval"
	// RebuildRequestTTL is how long a request (and its code) stays valid.
	RebuildRequestTTL = 30 * time.Minute
	// RebuildApprovalTTL is how long root's approval stays valid.
	RebuildApprovalTTL = 24 * time.Hour
	// approvalFutureSlack is how far ahead of the clock an approval may be
	// dated before it is refused.
	approvalFutureSlack = 5 * time.Minute
)

// RebuildRequest is what the agent asks root to approve: the code the
// panel shows and the source pin when it was made. Written by the agent only.
type RebuildRequest struct {
	Code    string    `json:"code"`   // six digits
	Source  string    `json:"source"` // host:port
	Created time.Time `json:"created"`
}

// RebuildApproval is root's approval, written by "dbpanel agent approve" only.
type RebuildApproval struct {
	Source   string    `json:"source"` // host:port of the source pin approved
	Addr     string    `json:"addr"`   // the IP address shown to the operator
	Approved time.Time `json:"approved"`
	Expires  time.Time `json:"expires"` // Approved + RebuildApprovalTTL
}

// SourceLookupError is a failed DNS lookup of the source's host; Source is
// the pinned host:port.
type SourceLookupError struct {
	Source string
	Err    error
}

func (e *SourceLookupError) Error() string {
	return fmt.Sprintf("alamat untuk nama sumber %s tidak ditemukan (DNS): %v", e.Source, e.Err)
}

func (e *SourceLookupError) Unwrap() error { return e.Err }

// NewRebuildApproval is the approval root gives at now.
func NewRebuildApproval(source, addr string, now time.Time) RebuildApproval {
	return RebuildApproval{Source: source, Addr: addr, Approved: now, Expires: now.Add(RebuildApprovalTTL)}
}

// newRebuildCode is a six-digit code from crypto/rand, leading zeros kept.
func newRebuildCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// sameSource reports whether a and b name the same host:port: the host
// compared without case, the port as a number.
func sameSource(a, b string) bool {
	ha, pa, err := ParseSource(a)
	if err != nil {
		return false
	}
	hb, pb, err := ParseSource(b)
	if err != nil {
		return false
	}
	return strings.EqualFold(ha, hb) && pa == pb
}

// validFor reports whether the request is for source and still in force.
func (r RebuildRequest) validFor(source string, now time.Time) bool {
	return sameSource(r.Source, source) &&
		now.Before(r.Created.Add(RebuildRequestTTL)) &&
		!r.Created.After(now.Add(approvalFutureSlack))
}

const approvalTimeFormat = "2006-01-02 15:04 MST"

// checkApproval is the rule of spec 5.2; ips are what the source's host
// resolves to now (the host itself when it is an IP). nil means valid; an
// error's text says why.
func checkApproval(a RebuildApproval, role, source string, ips []net.IP, now time.Time) error {
	if role != roleQuarantined {
		return fmt.Errorf("server ini tidak dikarantina (peran: %s)", role)
	}
	if !sameSource(a.Source, source) {
		return fmt.Errorf("persetujuan untuk sumber %s, bukan sumber yang dipin sekarang (%s)", a.Source, source)
	}
	// Every address the name resolves to must be the approved one: the
	// copy and the replication link connect by name and resolve again.
	approved := net.ParseIP(a.Addr)
	same := approved != nil && len(ips) > 0
	for _, ip := range ips {
		if !approved.Equal(ip) {
			same = false
			break
		}
	}
	if !same {
		return fmt.Errorf("nama sumber %s sekarang mengarah ke alamat lain, bukan alamat yang disetujui (%s)", source, a.Addr)
	}
	if a.Approved.After(now.Add(approvalFutureSlack)) {
		return fmt.Errorf("waktu persetujuan %s lebih dari 5 menit di masa depan; periksa jam server ini", a.Approved.Local().Format(approvalTimeFormat))
	}
	if a.Expires.Before(a.Approved) {
		return errors.New("berkas persetujuan tidak bisa dipakai: waktu habis lebih awal dari waktu persetujuan")
	}
	if a.Expires.Sub(a.Approved) > RebuildApprovalTTL {
		return errors.New("masa berlaku persetujuan lebih dari 24 jam")
	}
	if !now.Before(a.Expires) {
		return fmt.Errorf("persetujuan sudah habis pada %s", a.Expires.Local().Format(approvalTimeFormat))
	}
	return nil
}

// ReadRebuildRequest reads the request file; ok is false when it is absent.
func (p *Pins) ReadRebuildRequest() (RebuildRequest, bool, error) {
	var r RebuildRequest
	ok, err := readJSON(p.path(RebuildRequestFile), &r)
	if err != nil || !ok {
		return RebuildRequest{}, false, err
	}
	return r, true, nil
}

// pinnedSource is the source pin as host:port.
func (p *Pins) pinnedSource() (string, bool) {
	host, port, ok := p.Source()
	if !ok {
		return "", false
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), true
}

// ValidRebuildRequest is the request when it is valid for the current
// source pin and still in force.
func (p *Pins) ValidRebuildRequest(now time.Time) (RebuildRequest, bool, error) {
	r, ok, err := p.ReadRebuildRequest()
	if err != nil || !ok {
		return RebuildRequest{}, false, err
	}
	source, has := p.pinnedSource()
	if !has || !r.validFor(source, now) {
		return RebuildRequest{}, false, nil
	}
	return r, true, nil
}

// EnsureRebuildRequest returns the valid request, or writes a new one (new
// code) for the current source pin. Refused unless the role is quarantined
// with a source pin.
func (p *Pins) EnsureRebuildRequest(now time.Time) (RebuildRequest, error) {
	if role := p.Role(); role != roleQuarantined {
		return RebuildRequest{}, fmt.Errorf("server ini tidak dikarantina (peran: %s)", role)
	}
	source, has := p.pinnedSource()
	if !has {
		return RebuildRequest{}, errors.New("server ini tidak punya sumber replikasi yang dipin")
	}
	r, ok, err := p.ValidRebuildRequest(now)
	if err != nil {
		return RebuildRequest{}, err
	}
	if ok {
		return r, nil
	}
	code, err := newRebuildCode()
	if err != nil {
		return RebuildRequest{}, err
	}
	r = RebuildRequest{Code: code, Source: source, Created: now}
	if err := writeJSON(p.path(RebuildRequestFile), r); err != nil {
		return RebuildRequest{}, err
	}
	return r, nil
}

// RemoveRebuildRequest removes the request file; none is not an error.
func (p *Pins) RemoveRebuildRequest() error { return removeFile(p.path(RebuildRequestFile)) }

// RemoveRebuildApproval removes the approval file; none is not an error.
func (p *Pins) RemoveRebuildApproval() error { return removeFile(p.path(RebuildApprovalFile)) }

// ReadRebuildApproval reads the approval file; ok is false when it is absent.
func (p *Pins) ReadRebuildApproval() (RebuildApproval, bool, error) {
	var a RebuildApproval
	ok, err := readJSON(p.path(RebuildApprovalFile), &a)
	if err != nil || !ok {
		return RebuildApproval{}, false, err
	}
	return a, true, nil
}

// WriteRebuildApproval writes the approval file.
func (p *Pins) WriteRebuildApproval(a RebuildApproval) error {
	return writeJSON(p.path(RebuildApprovalFile), a)
}

// ClearRebuild removes the request and the approval; none is not an error.
func (p *Pins) ClearRebuild() error {
	for _, name := range []string{RebuildRequestFile, RebuildApprovalFile} {
		if err := removeFile(p.path(name)); err != nil {
			return err
		}
	}
	return nil
}

// sourceIPs is what host resolves to: the host itself when it is an IP.
func sourceIPs(ctx context.Context, host string) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}, nil
	}
	rctx, cancel := context.WithTimeout(ctx, peerTimeout)
	defer cancel()
	return lookupIP(rctx, host)
}

// CheckRebuildApproval reads the approval and checks it against the pins
// and the source host's addresses now (lookupIP, peerTimeout).
func (p *Pins) CheckRebuildApproval(ctx context.Context, now time.Time) (RebuildApproval, error) {
	a, ok, err := p.ReadRebuildApproval()
	if err != nil {
		return RebuildApproval{}, fmt.Errorf("berkas persetujuan tidak bisa dipakai: %v", err)
	}
	if !ok {
		return RebuildApproval{}, fmt.Errorf("berkas persetujuan tidak bisa dipakai: %v", errors.New("berkas tidak ada"))
	}
	role := p.Role()
	host, port, hasSrc := p.Source()
	source := ""
	if hasSrc {
		source = net.JoinHostPort(host, strconv.Itoa(port))
	}
	var ips []net.IP
	if hasSrc && role == roleQuarantined {
		if ips, err = sourceIPs(ctx, host); err != nil {
			return RebuildApproval{}, &SourceLookupError{Source: source, Err: err}
		}
	}
	if err := checkApproval(a, role, source, ips, now); err != nil {
		return RebuildApproval{}, err
	}
	return a, nil
}

// RebuildApprovalAddr is the address "approve" records and shows: the
// host when it is an IP, else the one address its name resolves to now
// (lookup; nil means the system resolver). The name is looked up even when
// an address was verified at quarantine (SourceAddrFile): a name with more
// than one address is SourceAddrsError, and one whose single address is
// not the verified one is SourceMovedError.
func (p *Pins) RebuildApprovalAddr(ctx context.Context, lookup func(context.Context, string) ([]net.IP, error)) (string, error) {
	host, port, dial, ok, err := p.SourceDial()
	if err != nil {
		return "", err
	}
	if !ok {
		return "", errors.New("server ini tidak punya sumber replikasi yang dipin")
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.String(), nil
	}
	if lookup == nil {
		lookup = lookupIP
	}
	source := net.JoinHostPort(host, strconv.Itoa(port))
	rctx, cancel := context.WithTimeout(ctx, peerTimeout)
	ips, err := lookup(rctx, host)
	cancel()
	if err != nil {
		return "", &SourceLookupError{Source: source, Err: err}
	}
	all := distinctAddrs(ips)
	switch {
	case len(all) == 0:
		return "", &SourceLookupError{Source: source, Err: errors.New("tidak ada alamat")}
	case len(all) > 1:
		return "", &SourceAddrsError{Source: source, Addrs: all}
	}
	if recorded := net.ParseIP(dial); recorded != nil && !recorded.Equal(net.ParseIP(all[0])) {
		return "", &SourceMovedError{Source: source, Addrs: all, Recorded: recorded.String()}
	}
	return all[0], nil
}

// distinctAddrs are ips as text without repeats, IPv4 before IPv6, each
// sorted.
func distinctAddrs(ips []net.IP) []string {
	ips = slices.Clone(ips)
	sort.Slice(ips, func(i, j int) bool {
		a4, b4 := ips[i].To4() != nil, ips[j].To4() != nil
		if a4 != b4 {
			return a4
		}
		return bytes.Compare(ips[i].To16(), ips[j].To16()) < 0
	})
	var all []string
	for _, ip := range ips {
		if ip == nil {
			continue
		}
		if s := ip.String(); len(all) == 0 || all[len(all)-1] != s {
			all = append(all, s)
		}
	}
	return all
}

// SourceAddrsError refuses an approval for a source name that resolves to
// more than one address (B16).
type SourceAddrsError struct {
	Source string
	Addrs  []string
}

func (e *SourceAddrsError) Error() string {
	return fmt.Sprintf("Nama sumber %s mengarah ke lebih dari satu alamat (%s). Persetujuan hanya bisa diberikan bila nama "+
		"itu mengarah ke satu alamat. Tetapkan satu alamat untuk nama itu di server ini (misalnya di /etc/hosts), lalu "+
		"jalankan perintah approve ini lagi. Persetujuan tidak ditulis.", e.Source, strings.Join(e.Addrs, ", "))
}

// SourceMovedError refuses an approval for a source name that no longer
// resolves to the address verified at quarantine (B15).
type SourceMovedError struct {
	Source   string
	Addrs    []string
	Recorded string
}

func (e *SourceMovedError) Error() string {
	return fmt.Sprintf("Nama sumber %s sekarang mengarah ke %s, bukan ke alamat yang dicatat saat karantina (%s). Bila "+
		"alamat baru itu memang master baru, jalankan: sudo dbpanel agent set-source %s, lalu jalankan perintah approve "+
		"ini lagi. Persetujuan tidak ditulis.", e.Source, strings.Join(e.Addrs, ", "), e.Recorded, e.Source)
}
