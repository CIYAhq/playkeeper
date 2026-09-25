package mcptools

import (
	"fmt"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
)

// phase is a server's state in a few words.
func phase(st api.ServerStatus) string {
	switch st.Phase {
	case api.PhaseOnline:
		return "online"
	case api.PhaseStopped:
		return "stopped"
	case api.PhaseCrashed:
		return "crashed"
	case api.PhaseStopping:
		return "stopping"
	case api.PhaseNotCreated:
		return "not set up yet"
	case api.PhaseDockerUnavailable:
		return "unavailable because Docker isn't running"
	case api.PhasePulling, api.PhaseStartingContainer, api.PhaseDownloading, api.PhaseStarting, api.PhasePreparingWorld:
		return "starting"
	}
	if st.Phase == "" {
		return "unknown"
	}
	return string(st.Phase)
}

// software is the server's software and Minecraft version, such as
// "Paper 1.21.8".
func software(cfg *api.ServerConfig) string {
	t := cfg.Type
	if t == "" {
		t = api.TypePaper
	}
	return strings.TrimSpace(strings.ToUpper(t[:1]) + t[1:] + " " + cfg.MinecraftVersion)
}

func when(t time.Time) string { return t.UTC().Format("2006-01-02 15:04 UTC") }

func size(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d bytes", n)
	}
	v, suffix := float64(n)/unit, "KB"
	for _, s := range []string{"MB", "GB", "TB"} {
		if v < unit {
			break
		}
		v, suffix = v/unit, s
	}
	return fmt.Sprintf("%.1f %s", v, suffix)
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func playersText(p *playersOut) string {
	if p.Online == 0 {
		return fmt.Sprintf("nobody is online (room for %d)", p.Max)
	}
	s := fmt.Sprintf("%d of %d players online", p.Online, p.Max)
	if len(p.Names) > 0 {
		s += ": " + strings.Join(p.Names, ", ")
	}
	return s
}

// resourcesText is the server's TPS, CPU and memory, as far as they're known.
func resourcesText(r *api.Resources) string {
	var parts []string
	if r.TPS != nil {
		parts = append(parts, fmt.Sprintf("%.1f TPS", *r.TPS))
	}
	if r.CPUPercent != nil {
		parts = append(parts, fmt.Sprintf("CPU %.0f%%", *r.CPUPercent))
	}
	if r.MemBytes != nil {
		m := "memory " + size(*r.MemBytes)
		if r.MemLimitBytes != nil && *r.MemLimitBytes > 0 {
			m += " of " + size(*r.MemLimitBytes)
		}
		parts = append(parts, m)
	}
	return strings.Join(parts, ", ")
}

// sentence joins a message and an optional hint.
func sentence(msg, hint string) string {
	if hint == "" {
		return msg
	}
	return msg + " " + hint
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
