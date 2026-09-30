// Package webservers names the web servers that listen on ports 80 and 443
// unless told otherwise. Playkeeper never takes those ports from one set to
// start with the machine: the installer leaves the dashboard on its own
// port then, and the agent opens neither port for the panel.
package webservers

import "path/filepath"

// Units are their systemd services.
var Units = []string{"nginx", "apache2", "httpd", "caddy", "lighttpd", "haproxy", "traefik", "openresty", "varnish", "h2o"}

// Enabled reports whether systemd starts unit with the machine: a link to
// its service in a .wants folder under systemdDir, such as
// /etc/systemd/system.
func Enabled(systemdDir, unit string) bool {
	found, _ := filepath.Glob(filepath.Join(systemdDir, "*.wants", unit+".service"))
	return len(found) > 0
}

// FirstEnabled is the first of Units systemd starts with the machine, or
// "" when none is.
func FirstEnabled(systemdDir string) string {
	for _, u := range Units {
		if Enabled(systemdDir, u) {
			return u
		}
	}
	return ""
}
