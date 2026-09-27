package install

import (
	"fmt"
	"strings"

	"github.com/CIYAhq/playkeeper/internal/platform"
)

// osCheck is the preflight's verdict on the operating system: a supported
// release passes, a newer release of a supported distribution only warns,
// and anything else needs --allow-untested-os.
func osCheck(o platform.OS, allowUntested bool) Check {
	c := Check{ID: "os", Label: "Operating system"}
	v := platform.Check(o)
	switch {
	case v.Support == platform.Tested && v.Release.SecurityEnded == "":
		c.Status, c.Detail = "pass", v.Release.Name+", which every release is tested on."
	case v.Support == platform.Tested:
		c.Status = "warn"
		c.Detail = fmt.Sprintf("%s: Playkeeper is tested on it, but its free security updates ended in %s.", v.Release.Name, v.Release.SecurityEnded)
		c.Fix = fmt.Sprintf("Move to %s when you can.", v.Distro.Newest().Name)
	case v.Support == platform.Newer && v.Release == v.Distro.Newest():
		c.Status = "warn"
		c.Detail = fmt.Sprintf("%s is newer than the newest tested release, %s. It should work, so the installer continues.", o.Display(), v.Release.Name)
	case v.Support == platform.Newer:
		c.Status = "warn"
		c.Detail = fmt.Sprintf("%s isn't a tested release, but it's newer than %s, which is. It should work, so the installer continues.", o.Display(), v.Release.Name)
	case allowUntested:
		c.Status = "warn"
		c.Detail = fmt.Sprintf("%s is not tested; continuing because --allow-untested-os was given.", o.Display())
	case v.Distro != nil:
		c.Status = "fail"
		c.Detail = fmt.Sprintf("%s is older than the releases Playkeeper supports: %s or later.", o.Display(), v.Distro.Oldest().Name)
		c.Fix = fmt.Sprintf("Upgrade to %s, or pass --allow-untested-os to try anyway (unsupported).", v.Distro.Newest().Name)
	default:
		c.Status = "fail"
		c.Detail = fmt.Sprintf("Found %s. Playkeeper runs on %s.", o.Display(), platform.Summary())
		c.Fix = "Use one of those, or pass --allow-untested-os to try anyway (unsupported)."
	}
	return c
}

// archiveName is whose package archive Docker comes from, like "Debian's".
func archiveName(o platform.OS) string {
	if n := o.DistroName(); n != "" {
		return n + "'s"
	}
	return "the distribution's"
}

// dockerPackages are the packages that give this machine Docker: docker.io,
// and the docker command where the distribution packages it on its own,
// as Debian 13 does with docker-cli, which docker.io only recommends.
func dockerPackages(sys System) []string {
	pkgs := []string{"docker.io"}
	if aptCandidate(sys, "docker-cli") {
		pkgs = append(pkgs, "docker-cli")
	}
	return pkgs
}

// aptCandidate reports whether apt can install pkg itself, not only as a
// name another package provides (Ubuntu's docker.io provides docker-cli).
func aptCandidate(sys System, pkg string) bool {
	out, err := sys.Run("apt-cache", "policy", pkg)
	if err != nil {
		return false
	}
	for _, l := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), "Candidate:"); ok {
			v = strings.TrimSpace(v)
			return v != "" && v != "(none)"
		}
	}
	return false
}
