package handlers

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// registrationLimiter enforces a per-IP rate limit on local account registration.
// The first attempt from an IP is allowed immediately; subsequent attempts must
// wait a delay that starts at 10 seconds and doubles on every attempt.

type regRecord struct {
	delay      time.Duration
	allowAfter time.Time
}

const regBaseDelay = 10 * time.Second

var (
	regMu      sync.Mutex
	regRecords = make(map[string]*regRecord)
)

func init() {
	// Background cleanup: remove records older than 24 hours.
	go func() {
		for range time.Tick(10 * time.Minute) {
			now := time.Now()
			regMu.Lock()
			for ip, rec := range regRecords {
				if now.Sub(rec.allowAfter) > 24*time.Hour {
					delete(regRecords, ip)
				}
			}
			regMu.Unlock()
		}
	}()
}

// clientIP extracts the best-guess client IP from a request.
func clientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		if host, _, err := net.SplitHostPort(fwd); err == nil {
			return host
		}
		return fwd
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// checkRegistrationLimit returns true (allowed) or false (rate-limited).
// On each call it records the attempt and doubles the future delay.
func checkRegistrationLimit(ip string) (allowed bool, retryAfter time.Duration) {
	regMu.Lock()
	defer regMu.Unlock()

	now := time.Now()
	rec, exists := regRecords[ip]

	if !exists {
		// First attempt — allow, set delay for next attempt.
		regRecords[ip] = &regRecord{
			delay:      regBaseDelay,
			allowAfter: now.Add(regBaseDelay),
		}
		return true, 0
	}

	if now.Before(rec.allowAfter) {
		return false, rec.allowAfter.Sub(now)
	}

	// Attempt allowed — double the delay for next time.
	rec.delay *= 2
	rec.allowAfter = now.Add(rec.delay)
	return true, 0
}
