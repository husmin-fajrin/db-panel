package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DeviceCookieName is the cookie a successful login sets so the browser's
// later logins have their own attempt budget (spec 5.1). It is not a
// credential: the password is still asked for.
const DeviceCookieName = "__Host-dbpanel_device"

// DeviceCookieMaxAge is how long a device cookie lives and is accepted.
const DeviceCookieMaxAge = 365 * 24 * time.Hour

// deviceVersion is the first field of a device cookie's value.
const deviceVersion = "v1"

// deviceIDBytes is the random length of a device ID (32 hex characters).
const deviceIDBytes = 16

// Device is what a device cookie says: the username it was issued for, a
// random ID (32 hex characters) and when it was issued (whole seconds).
type Device struct {
	Username, ID string
	Issued       time.Time
}

// NewDevice issues a device cookie value for username:
// v1.<base64url username>.<32 hex ID>.<unix seconds issued>.<base64url HMAC-SHA256>,
// the HMAC taken with key over everything before its own dot.
func NewDevice(key []byte, username string, now time.Time) (value string, d Device) {
	raw := make([]byte, deviceIDBytes)
	rand.Read(raw) // never fails (crypto/rand)
	d = Device{Username: username, ID: hex.EncodeToString(raw), Issued: time.Unix(now.Unix(), 0)}
	body := strings.Join([]string{deviceVersion, base64.RawURLEncoding.EncodeToString([]byte(username)), d.ID,
		strconv.FormatInt(d.Issued.Unix(), 10)}, ".")
	return body + "." + base64.RawURLEncoding.EncodeToString(deviceMAC(key, body)), d
}

// ParseDevice checks a device cookie value: its form, its signature under
// key, and that it was issued at most DeviceCookieMaxAge before now. One
// issued after now stays valid (a clock set back does not void it). The
// caller checks the username and whether it was issued before the last
// password reset.
func ParseDevice(key []byte, value string, now time.Time) (Device, bool) {
	parts := strings.Split(value, ".")
	if len(parts) != 5 || parts[0] != deviceVersion {
		return Device{}, false
	}
	mac, err := base64.RawURLEncoding.DecodeString(parts[4])
	body := value[:len(value)-len(parts[4])-1]
	if err != nil || !hmac.Equal(mac, deviceMAC(key, body)) {
		return Device{}, false
	}
	user, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(parts[2]) != 2*deviceIDBytes || strings.ToLower(parts[2]) != parts[2] {
		return Device{}, false
	}
	if _, err := hex.DecodeString(parts[2]); err != nil {
		return Device{}, false
	}
	secs, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil {
		return Device{}, false
	}
	issued := time.Unix(secs, 0)
	if now.Sub(issued) > DeviceCookieMaxAge {
		return Device{}, false
	}
	return Device{Username: string(user), ID: parts[2], Issued: issued}, true
}

func deviceMAC(key []byte, body string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(body))
	return m.Sum(nil)
}

// SetDeviceCookie sets the device cookie: "__Host-" (Secure, Path=/, no
// Domain), HttpOnly, SameSite=Lax, for DeviceCookieMaxAge.
func SetDeviceCookie(w http.ResponseWriter, value string) {
	http.SetCookie(w, &http.Cookie{
		Name:     DeviceCookieName,
		Value:    value,
		Path:     "/",
		MaxAge:   int(DeviceCookieMaxAge / time.Second),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   true,
	})
}

// DeviceCookie returns the device cookie's value the request carries.
func DeviceCookie(r *http.Request) (string, bool) {
	if c, err := r.Cookie(DeviceCookieName); err == nil && c.Value != "" {
		return c.Value, true
	}
	return "", false
}
