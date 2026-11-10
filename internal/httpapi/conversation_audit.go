package httpapi

import (
	"net/http"
	"sync"
	"time"
)

const controlAuditLimit = 200

type controlAuditEntry struct {
	At         int64  `json:"at"`
	Caller     string `json:"caller"`
	Tool       string `json:"tool"`
	Target     string `json:"target,omitempty"`
	Outcome    string `json:"outcome"`
	DurationMS int64  `json:"durationMs"`
}

// controlAudit is a bounded in-memory ring of control calls. It is a UI trail,
// not durable accounting; restarting the server clears it.
type controlAudit struct {
	mu      sync.Mutex
	entries []controlAuditEntry
}

func (a *controlAudit) add(entry controlAuditEntry) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, entry)
	if len(a.entries) > controlAuditLimit {
		a.entries = append([]controlAuditEntry(nil), a.entries[len(a.entries)-controlAuditLimit:]...)
	}
}

func (a *controlAudit) list(caller, target string, limit int) []controlAuditEntry {
	if limit <= 0 || limit > controlAuditLimit {
		limit = 50
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]controlAuditEntry, 0, limit)
	for i := len(a.entries) - 1; i >= 0 && len(out) < limit; i-- {
		entry := a.entries[i]
		if caller != "" && entry.Caller != caller {
			continue
		}
		if target != "" && entry.Target != target {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// controlLimiter is a tiny per-key token bucket. It avoids a dependency on
// golang.org/x/time/rate for three fixed budgets.
type controlLimiter struct {
	mu      sync.Mutex
	buckets map[string]*controlBucket
}

type controlBucket struct {
	tokens float64
	last   time.Time
}

func (l *controlLimiter) allow(key string, ratePerSecond, burst float64) (bool, time.Duration) {
	if key == "" {
		return true, 0
	}
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.buckets == nil {
		l.buckets = map[string]*controlBucket{}
	}
	if len(l.buckets) > 512 {
		for k, b := range l.buckets {
			if now.Sub(b.last) > 10*time.Minute {
				delete(l.buckets, k)
			}
		}
	}
	b := l.buckets[key]
	if b == nil {
		b = &controlBucket{tokens: burst, last: now}
		l.buckets[key] = b
	}
	elapsed := now.Sub(b.last).Seconds()
	b.last = now
	b.tokens = min(burst, b.tokens+elapsed*ratePerSecond)
	if b.tokens < 1 {
		wait := (1 - b.tokens) / ratePerSecond
		return false, time.Duration(wait * float64(time.Second))
	}
	b.tokens--
	return true, 0
}

type controlResponseRecorder struct {
	http.ResponseWriter
	status int
}

func (r *controlResponseRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *controlResponseRecorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(p)
}
