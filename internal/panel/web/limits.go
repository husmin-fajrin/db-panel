package web

import (
	"crypto/sha256"
	"encoding/hex"
	"net/netip"
	"sync"
	"time"
)

// Attempt limits (spec 5.1, R-181): traffic without a login never touches
// the budgets a logged-in session or a browser that has logged in uses.
const (
	// Wrong passwords per session at POST /confirm, when the Deps
	// limiters are nil.
	defaultConfirmMax        = 5
	defaultConfirmWindow     = time.Minute
	defaultConfirmLongMax    = 20
	defaultConfirmLongWindow = 15 * time.Minute
	// Logins with a valid device cookie, when the Deps limiters are nil:
	// per cookie and address, and wrong passwords per cookie for all
	// addresses.
	defaultDevicePairMax  = 5
	defaultDeviceTotalMax = 20
	defaultDeviceWindow   = 15 * time.Minute
	// deviceLoginConcurrency is the number of password checks that logins
	// with a valid device cookie run at once, apart from loginSem; such a
	// login waits at most deviceSlotWait for one (K7).
	deviceLoginConcurrency = 2
	deviceSlotWait         = 5 * time.Second
	// maxDeviceSucceededPairs bounds succeededPairs.
	maxDeviceSucceededPairs = 10000
)

// sessionKey is a session's limiter key: the hex SHA-256 of its token, so
// the token itself is never a key of a limiter map (K3).
func sessionKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ipKey is an address's limiter key: an IPv4 address unchanged, an IPv6
// one as its /64 prefix followed by "/64" (one host usually holds a whole
// /64). Anything that does not parse is used as it is.
func ipKey(ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil || a.Is4() || a.Is4In6() {
		return ip
	}
	p, err := a.WithZone("").Prefix(64)
	if err != nil {
		return ip
	}
	return p.Addr().String() + "/64"
}

// devicePairKey is the DevicePairLimiter key of a device ID and an address.
func devicePairKey(id, ip string) string { return id + "|" + ipKey(ip) }

// succeededPairs remembers the device pairs (devicePairKey) that have
// logged in since the panel started, at most maxDeviceSucceededPairs: a
// pair that has is not refused when its cookie's total is used up.
type succeededPairs struct {
	mu sync.Mutex
	m  map[string]struct{}
}

func (s *succeededPairs) has(pair string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.m[pair]
	return ok
}

// add records pair; when the map is full an arbitrary other pair is
// dropped (only right passwords add pairs).
func (s *succeededPairs) add(pair string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = make(map[string]struct{})
	}
	if _, ok := s.m[pair]; ok {
		return
	}
	if len(s.m) >= maxDeviceSucceededPairs {
		for k := range s.m {
			delete(s.m, k)
			break
		}
	}
	s.m[pair] = struct{}{}
}
