package install

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// shell makes the fake host's /bin/sh an ELF file of class 1 (32-bit) or 2
// (64-bit); only its header is read.
func shell(t *testing.T, h *fakeHost, class byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(h.root, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	header := append([]byte("\x7fELF"), class, 1, 1, 0)
	if err := os.WriteFile(filepath.Join(h.root, "bin/sh"), append(header, make([]byte, 56)...), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestPreflightPassesOnBothCPUsReleasesAreBuiltFor(t *testing.T) {
	for arch, detail := range map[string]string{"amd64": "x86_64 (amd64).", "arm64": "64-bit ARM (arm64)."} {
		h := newFakeHost(t)
		shell(t, h, 2)
		sys := h.system(t)
		sys.Arch = func() string { return arch }
		f := Preflight(context.Background(), sys, opts(""))
		if c := check(f, "arch"); !f.OK() || c == nil || c.Status != "pass" || c.Detail != detail {
			var buf bytes.Buffer
			PrintChecks(&buf, f)
			t.Errorf("%s: the CPU check says %+v:\n%s", arch, c, buf.String())
		}
		if err := upgradeChecks(sys, Options{}); err != nil {
			t.Errorf("%s: an upgrade is refused: %v", arch, err)
		}
	}
}

func TestPreflightRefusesA32BitSystemOnA64BitCPU(t *testing.T) {
	h := newFakeHost(t)
	shell(t, h, 1)
	before := snapshot(t, h.root)
	sys := h.system(t)
	sys.Arch = func() string { return "arm64" }
	for _, untested := range []bool{false, true} {
		o := opts("")
		o.AllowUntestedOS = untested
		f := Preflight(context.Background(), sys, o)
		c := check(f, "arch")
		if f.OK() || c == nil || c.Status != "fail" || !strings.Contains(c.Detail, "32-bit system on a 64-bit CPU") || !strings.Contains(c.Fix, "64-bit version of the operating system") {
			t.Fatalf("--allow-untested-os %v: %+v", untested, c)
		}
	}
	if _, err := Run(context.Background(), sys, Options{PanelPort: 8443, GamePort: 25565, Yes: true, In: strings.NewReader(""), Out: &bytes.Buffer{}}, "test"); err == nil {
		t.Fatal("the install went ahead on a 32-bit system")
	}
	if d := diff(before, snapshot(t, h.root)); len(d) != 0 {
		t.Fatalf("the refused install changed the host: %v", d)
	}
}

func TestPreflightRefusesOtherCPUsUnlessUntestedIsAllowed(t *testing.T) {
	h := newFakeHost(t)
	sys := h.system(t)
	sys.Arch = func() string { return "riscv64" }
	f := Preflight(context.Background(), sys, opts(""))
	if c := check(f, "arch"); f.OK() || c == nil || c.Status != "fail" || !strings.Contains(c.Detail, "x86_64 (amd64) and 64-bit ARM (arm64)") || c.Fix == "" {
		t.Fatalf("riscv64: %+v", c)
	}
	o := opts("")
	o.AllowUntestedOS = true
	if c := check(Preflight(context.Background(), sys, o), "arch"); c == nil || c.Status != "warn" {
		t.Fatalf("riscv64 with --allow-untested-os: %+v", c)
	}
	if err := upgradeChecks(sys, Options{}); err == nil || !strings.Contains(err.Error(), "Nothing was changed") {
		t.Fatalf("an upgrade on riscv64: %v", err)
	}
}
