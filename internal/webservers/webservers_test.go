package webservers

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAWebServerCountsOnlyWhileItStartsWithTheMachine(t *testing.T) {
	dir := t.TempDir()
	if got := FirstEnabled(dir); got != "" {
		t.Fatalf("with nothing enabled: %q", got)
	}
	wants := filepath.Join(dir, "multi-user.target.wants")
	os.MkdirAll(wants, 0o755)
	os.Symlink("/lib/systemd/system/sshd.service", filepath.Join(wants, "sshd.service"))
	os.Symlink("/lib/systemd/system/caddy.service", filepath.Join(wants, "caddy.service"))
	if got := FirstEnabled(dir); got != "caddy" || !Enabled(dir, "caddy") || Enabled(dir, "nginx") {
		t.Fatalf("with caddy enabled: %q", got)
	}
}
