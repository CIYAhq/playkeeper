package service

import (
	"bufio"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/CIYAhq/playkeeper/internal/names"
)

// limiter is a keyed token bucket. Buckets that have refilled completely
// are forgotten, so memory follows recent traffic.
type limiter struct {
	mu       sync.Mutex
	capacity float64
	refill   float64 // tokens per second
	buckets  map[string]*bucket
	gcAt     int
	now      func() time.Time
}

// minGC is the number of buckets below which a limiter never collects.
const minGC = 10000

type bucket struct {
	tokens float64
	last   time.Time
}

// newLimiter allows n events per period on average, and up to burst at once.
func newLimiter(burst, n int, per time.Duration, now func() time.Time) *limiter {
	return &limiter{capacity: float64(burst), refill: float64(n) / per.Seconds(), buckets: map[string]*bucket{}, gcAt: minGC, now: now}
}

// allow takes one token for key; when there is none it returns the wait.
func (l *limiter) allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b := l.buckets[key]
	if b == nil {
		// Collecting only once the map has doubled keeps the cost of a
		// flood of new keys linear.
		if len(l.buckets) >= l.gcAt {
			l.gc(now)
			l.gcAt = max(minGC, 2*len(l.buckets))
		}
		b = &bucket{tokens: l.capacity, last: now}
		l.buckets[key] = b
	}
	b.tokens = math.Min(l.capacity, b.tokens+now.Sub(b.last).Seconds()*l.refill)
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, time.Duration((1 - b.tokens) / l.refill * float64(time.Second))
}

func (l *limiter) gc(now time.Time) {
	for k, b := range l.buckets {
		if now.Sub(b.last).Seconds()*l.refill >= l.capacity {
			delete(l.buckets, k)
		}
	}
}

// reserved are names nobody can claim and whose records the service never
// touches: the project's own hosts, mail and infrastructure names, and
// names people would trust as official.
var reserved = func() map[string]bool {
	m := map[string]bool{}
	for _, n := range strings.Fields(`
		www www1 www2 names name api app apps admin administrator root install get download downloads
		update updates release releases status docs doc help support faq about blog news forum wiki
		demo test staging dev beta preview static assets cdn media files images
		mail email smtp imap pop pop3 mx webmail autoconfig autodiscover mta-sts postmaster hostmaster
		webmaster abuse security noreply no-reply dmarc dkim spf bimi ns ns1 ns2 ns3 ns4 dns
		wpad isatap localhost broadcasthost ftp sftp ssh vpn proxy gateway router
		login signin signup register account accounts auth sso oauth verify billing pay payment payments
		shop store dashboard panel console portal official team staff moderator mod owner
		minecraft mojang microsoft papermc paper purpur fabric forge neoforge spigot bukkit velocity
		cloudflare namecheap coolify github`) {
		m[n] = true
	}
	return m
}()

// reservedName reports whether the built-in list reserves name. Anything
// containing "playkeeper" is reserved too, so nobody can pose as the project.
func reservedName(name string) bool {
	return reserved[name] || strings.Contains(name, "playkeeper")
}

// maxBlocklist bounds the blocklist file.
const maxBlocklist = 1 << 20

// blocklist is the owner's list of names nobody may claim, reloaded when the
// file changes.
type blocklist struct {
	path string

	mu    sync.RWMutex
	names map[string]bool
	mod   time.Time
	size  int64
}

func (b *blocklist) has(name string) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.names[name]
}

// reload reads the file again if it changed. Lines are names; blank lines
// and lines starting with # are ignored, and so are lines that are not
// valid names.
func (b *blocklist) reload(log *slog.Logger) {
	if b.path == "" {
		return
	}
	fi, err := os.Stat(b.path)
	if errors.Is(err, fs.ErrNotExist) {
		b.set(nil, time.Time{}, -1)
		return
	}
	if err != nil {
		log.Warn("Could not read the blocklist", "file", b.path, "error", err)
		return
	}
	b.mu.RLock()
	same := fi.ModTime().Equal(b.mod) && fi.Size() == b.size
	b.mu.RUnlock()
	if same {
		return
	}
	f, err := os.Open(b.path)
	if err != nil {
		log.Warn("Could not read the blocklist", "file", b.path, "error", err)
		return
	}
	defer f.Close()
	set := map[string]bool{}
	skipped := 0
	sc := bufio.NewScanner(io.LimitReader(f, maxBlocklist))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if n := strings.ToLower(line); names.CheckName(n) == nil {
			set[n] = true
		} else {
			skipped++
		}
	}
	if err := sc.Err(); err != nil {
		log.Warn("Could not read the blocklist", "file", b.path, "error", err)
		return
	}
	b.set(set, fi.ModTime(), fi.Size())
	log.Info("Loaded the blocklist", "file", b.path, "names", len(set), "skipped", skipped)
}

func (b *blocklist) set(m map[string]bool, mod time.Time, size int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.names, b.mod, b.size = m, mod, size
}
