package install

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

// What dnf prints as it gives up: AlmaLinux 9's on 1 Oct 2026, when the
// extras repository's mirrors were part-way through a sync; and, as dnf 4.14
// on AlmaLinux 9.8 printed them against a local mirror, for a package no
// mirror sent, a package no repository has, and a package that fails its
// signature check after dnf got it from the next mirror.
const (
	dnfNoMirrorHadMetadata = "Error: Failed to download metadata for repo 'extras': Yum repo downloading error: Downloading error(s): repodata/4a21bf8eeb47d833caaebca30efd66e0a793131b21da17553e03b677347a9635-comps-extras.x86_64.xml - Cannot download, all mirrors were already tried without success\n"
	dnfNoMirrorHadPackage  = "Downloading Packages:\n[MIRROR] containerd.io-1.7.27-3.1.el9.x86_64.rpm: Status code: 404 for https://download.docker.com/linux/centos/9/x86_64/stable/Packages/containerd.io-1.7.27-3.1.el9.x86_64.rpm (IP: 203.0.113.90)\n[FAILED] containerd.io-1.7.27-3.1.el9.x86_64.rpm: No more mirrors to try - All mirrors were already tried without success\nThe downloaded packages were saved in cache until the next successful transaction.\nYou can remove cached packages by executing 'dnf clean packages'.\nError: Error downloading packages:\n  containerd.io-1.7.27-3.1.el9.x86_64: Cannot download, all mirrors were already tried without success\n"
	dnfNoMatch             = "No match for argument: docker-ce\nError: Unable to find a match: docker-ce\n"
	dnfBadSignature        = "Downloading Packages:\n[MIRROR] containerd.io-1.7.27-3.1.el9.x86_64.rpm: Curl error (52): Server returned nothing (no headers, no data) for https://download.docker.com/linux/centos/9/x86_64/stable/Packages/containerd.io-1.7.27-3.1.el9.x86_64.rpm [Empty reply from server]\ncontainerd.io-1.7.27-3.1.el9.x86_64.rpm          35 MB/s |  44 MB     00:01\nPackage containerd.io-1.7.27-3.1.el9.x86_64.rpm is not signed\nThe downloaded packages were saved in cache until the next successful transaction.\nYou can remove cached packages by executing 'dnf clean packages'.\nError: GPG check FAILED\n"
)

// The same as dnf 5.2 on Fedora 42 printed them against the local mirror,
// lines from its progress cut at 80 columns as it cuts them: for metadata no
// mirror had, which it takes for a repository without packages; for a package
// no mirror sent; and for a package that fails its signature check after dnf
// got it from the next mirror.
const (
	dnf5NoMirrorHadMetadata = "Updating and loading repositories:\n Docker CE Stable (added by Playkeeper)  100% | 759.0 KiB/s |   3.0 KiB |  00m00s\n>>> Status code: 404 for https://download.docker.com/linux/centos/10/x86_64/stab\n>>> No more mirrors to try - All mirrors were already tried without success     \nRepositories loaded.\nFailed to resolve the transaction:\nNo match for argument: docker-ce\nYou can try to add to command line:\n  --skip-unavailable to skip unavailable packages\n"
	dnf5NoMirrorHadPackage  = "[1/3] containerd.io-0:1.7.27-3.1.el10.x86_64 100% |   3.3 KiB/s |  10.0   B |  00m00s\n>>> Status code: 404 for https://download.docker.com/linux/centos/10/x86_64/stab\n>>> No more mirrors to try - All mirrors were already tried without success     \n--------------------------------------------------------------------------------\n[3/3] Total                             100% |   2.4 KiB/s |  10.0   B |  00m00s\nFailed to download packages\n Librepo error: Cannot download containerd.io-1.7.27-3.1.el10.x86_64.rpm: All mirrors were tried\n"
	dnf5BadSignature        = "[3/3] containerd.io-0:1.7.27-3.1.el10.x86_64 100% |  35.0 MiB/s |  44.0 MiB |  00m01s\n>>> Curl error (52): Server returned nothing (no headers, no data) for https://d\n--------------------------------------------------------------------------------\n[3/3] Total                             100% |  35.0 MiB/s |  44.0 MiB |  00m01s\nRunning transaction\nTransaction failed: Signature verification failed.\nOpenPGP check for package \"containerd.io-1.7.27-3.1.el10.x86_64\" (/var/cache/libdnf5/playkeeper-docker-ce-eec3c796af9bdc51/packages/containerd.io-1.7.27-3.1.el10.x86_64.rpm) from repo \"playkeeper-docker-ce\" has failed: The package is not signed.\n"
)

// What apt-get prints as it fails: lists from a mirror part-way through a
// sync, as apt 2.8 printed them in a test against such a mirror; a package
// the lists name that the mirror no longer has; a package no list has; and a
// mirror it couldn't reach next to a repository that isn't signed.
const (
	aptListsMidSync = "Hit:1 http://archive.ubuntu.com/ubuntu noble InRelease\nGet:2 http://archive.ubuntu.com/ubuntu noble-updates InRelease [126 kB]\nGet:3 http://archive.ubuntu.com/ubuntu noble-updates/universe amd64 Packages [1129 kB]\nErr:3 http://archive.ubuntu.com/ubuntu noble-updates/universe amd64 Packages\n  File has unexpected size (1128402 != 1129881). Mirror sync in progress? [IP: 203.0.113.80 80]\nFetched 126 kB in 1s (98.4 kB/s)\nReading package lists...\nE: Failed to fetch http://archive.ubuntu.com/ubuntu/dists/noble-updates/universe/binary-amd64/Packages.gz  File has unexpected size (1128402 != 1129881). Mirror sync in progress? [IP: 203.0.113.80 80]\n   Hashes of expected file:\n    - Filesize:1129881 [weak]\n   Release file created at: Thu, 01 Oct 2026 17:52:11 +0000\nE: Some index files failed to download. They have been ignored, or old ones used instead.\n"
	aptPackageGone  = "Reading package lists...\nBuilding dependency tree...\nReading state information...\nThe following NEW packages will be installed:\n  containerd docker.io pigz runc\nErr:2 http://archive.ubuntu.com/ubuntu noble-updates/universe amd64 docker.io amd64 27.5.1-0ubuntu3~24.04.2\n  404  Not Found [IP: 203.0.113.80 80]\nE: Failed to fetch http://archive.ubuntu.com/ubuntu/pool/universe/d/docker.io-app/docker.io_27.5.1-0ubuntu3~24.04.2_amd64.deb  404  Not Found [IP: 203.0.113.80 80]\nE: Unable to fetch some archives, maybe run apt-get update or try with --fix-missing?\n"
	aptNoPackage    = "Reading package lists...\nBuilding dependency tree...\nReading state information...\nE: Unable to locate package docker.io\n"
	aptNotSigned    = "Err:4 http://ppa.example.com/ubuntu noble InRelease\n  Could not connect to ppa.example.com:80 (203.0.113.81). - connect (111: Connection refused)\nReading package lists...\nE: The repository 'http://apt.example.com/ubuntu noble Release' is not signed.\nW: Failed to fetch http://ppa.example.com/ubuntu/dists/noble/InRelease  Could not connect to ppa.example.com:80 (203.0.113.81). - connect (111: Connection refused)\nW: Some index files failed to download. They have been ignored, or old ones used instead.\n"
)

const mirrorRetryLine = "    a package mirror wasn't ready; trying again...\n"

func TestDNFTriesAgainWithFreshMetadataOnlyWhenNoMirrorHadWhatItNeeded(t *testing.T) {
	for _, c := range []struct {
		name  string
		fails []string
		tries int
		ok    bool
	}{
		{"an install that works runs once", nil, 1, true},
		{"metadata no mirror had is asked for again", []string{dnfNoMirrorHadMetadata}, 2, true},
		{"a package no mirror sent is asked for again", []string{dnfNoMirrorHadPackage}, 2, true},
		{"mirrors that keep failing get four tries", []string{dnfNoMirrorHadMetadata, dnfNoMirrorHadPackage, dnfNoMirrorHadMetadata, dnfNoMirrorHadMetadata}, 4, false},
		{"a package no repository has fails at once", []string{dnfNoMatch}, 1, false},
		{"a signature that fails after a mirror dnf moved on from fails at once", []string{dnfBadSignature}, 1, false},
		{"dnf 5: metadata no mirror had is asked for again", []string{dnf5NoMirrorHadMetadata}, 2, true},
		{"dnf 5: a package no mirror sent is asked for again", []string{dnf5NoMirrorHadPackage}, 2, true},
		{"dnf 5: a signature that fails after a mirror dnf moved on from fails at once", []string{dnf5BadSignature}, 1, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newELHost(t, "almalinux", "9.6", "AlmaLinux")
			h.dnfFails = slices.Clone(c.fails)
			start, out := h.clock, &bytes.Buffer{}
			err := dnf{}.install(context.Background(), h.system(t), out, []string{"docker-ce", "docker-ce-cli", "containerd.io"}, nil)
			var tries []string
			for _, cmd := range h.cmds {
				if strings.HasPrefix(cmd, "dnf ") {
					tries = append(tries, cmd)
				}
			}
			want := []string{"dnf -y --setopt=install_weak_deps=False install docker-ce docker-ce-cli containerd.io"}
			for len(want) < c.tries {
				want = append(want, "dnf -y --refresh --setopt=install_weak_deps=False install docker-ce docker-ce-cli containerd.io")
			}
			if !slices.Equal(tries, want) {
				t.Fatalf("dnf ran %q; want %q, each try after a mirror failure reading the metadata afresh", tries, want)
			}
			checkMirrorRetry(t, err, c.ok, c.fails, out.String(), h.clock.Sub(start), c.tries)
		})
	}
}

func TestAPTTriesAgainWithFreshListsOnlyWhenTheMirrorDidntHaveWhatItNeeded(t *testing.T) {
	update := "apt-get -o DPkg::Lock::Timeout=60 update"
	install := "apt-get -o DPkg::Lock::Timeout=60 install -y --no-install-recommends docker.io docker-cli"
	for _, c := range []struct {
		name                 string
		updateFails, install []string
		want                 []string
		ok                   bool
	}{
		{"an install that works runs once", nil, nil, []string{update, install}, true},
		{"lists from a mirror part-way through a sync are read again", []string{aptListsMidSync}, nil, []string{update, update, install}, true},
		{"a package the mirror no longer has is fetched again after fresh lists", nil, []string{aptPackageGone}, []string{update, install, update, install}, true},
		{"mirrors that keep failing get four tries", []string{aptListsMidSync, aptListsMidSync}, []string{aptPackageGone, aptPackageGone}, []string{update, update, update, install, update, install}, false},
		{"a package no list has fails at once", nil, []string{aptNoPackage}, []string{update, install}, false},
		{"a mirror apt couldn't reach, next to a repository that isn't signed, fails at once", []string{aptNotSigned}, nil, []string{update}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newFakeHost(t)
			h.aptPolicy = map[string]string{"docker-cli": "docker-cli:\n  Installed: (none)\n  Candidate: 26.1.5+dfsg1-9+deb13u1\n"}
			h.aptUpdateFails, h.aptInstallFails = slices.Clone(c.updateFails), slices.Clone(c.install)
			start, out := h.clock, &bytes.Buffer{}
			err := apt{}.install(context.Background(), h.system(t), out, []string{"docker.io"}, []string{"docker-cli"})
			var ran []string
			for _, cmd := range h.cmds {
				if strings.HasPrefix(cmd, "apt-get ") {
					ran = append(ran, cmd)
				}
			}
			if !slices.Equal(ran, c.want) {
				t.Fatalf("apt-get ran %q; want %q, each try after a mirror failure updating the lists first", ran, c.want)
			}
			checkMirrorRetry(t, err, c.ok, append(slices.Clone(c.updateFails), c.install...), out.String(), h.clock.Sub(start), strings.Count(strings.Join(ran, "\n"), update))
		})
	}
}

// checkMirrorRetry checks what an install that ran tries times says and
// waits: one line when it tried again, 15, 30 and 60 seconds between tries,
// and the last failure's own words when it gave up.
func checkMirrorRetry(t *testing.T, err error, ok bool, fails []string, out string, waited time.Duration, tries int) {
	t.Helper()
	if ok != (err == nil) {
		t.Fatalf("install: %v; want it to work: %v", err, ok)
	}
	if !ok {
		last := strings.TrimSpace(fails[len(fails)-1])
		if last = last[strings.LastIndex(last, "\n")+1:]; !strings.Contains(err.Error(), last) {
			t.Fatalf("a failed install must say why: %v; want %q", err, last)
		}
	}
	want := map[int]time.Duration{1: 0, 2: 15 * time.Second, 3: 45 * time.Second, 4: 105 * time.Second}[tries]
	if waited != want {
		t.Fatalf("waited %s over %d tries; want %s", waited, tries, want)
	}
	if line := map[bool]string{true: mirrorRetryLine}[tries > 1]; out != line {
		t.Fatalf("the install said %q; want %q, once at most", out, line)
	}
}

func TestAnInstallInterruptedWhileItWaitsToTryAgainStopsThere(t *testing.T) {
	for _, c := range []struct {
		name       string
		host       func(t *testing.T) *fakeHost
		pm         packageManager
		tries, why string
	}{
		{"dnf", func(t *testing.T) *fakeHost {
			h := newELHost(t, "almalinux", "9.6", "AlmaLinux")
			h.dnfFails = []string{dnfNoMirrorHadMetadata}
			return h
		}, dnf{}, "dnf ", "Failed to download metadata"},
		{"apt", func(t *testing.T) *fakeHost {
			h := newFakeHost(t)
			h.aptUpdateFails = []string{aptListsMidSync}
			return h
		}, apt{}, "apt-get -o DPkg::Lock::Timeout=60 update", "E: Some index files failed to download"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := c.host(t)
			ctx, interrupt := context.WithCancel(context.Background())
			sys := h.system(t)
			sleep := sys.Sleep
			sys.Sleep = func(d time.Duration) { interrupt(); sleep(d) }
			start := h.clock
			err := c.pm.install(ctx, sys, &bytes.Buffer{}, []string{"docker"}, nil)
			tries := 0
			for _, cmd := range h.cmds {
				if strings.HasPrefix(cmd, c.tries) {
					tries++
				}
			}
			if err == nil || !strings.Contains(err.Error(), c.why) || tries != 1 {
				t.Fatalf("an install interrupted as it waits must stop with the mirror's failure, without trying again: %v, %d tries", err, tries)
			}
			if waited := h.clock.Sub(start); waited != time.Second {
				t.Fatalf("it waited %s after the interruption; want it to stop within the second", waited)
			}
		})
	}
}

func TestAnInstallSaysOnceThatAPackageMirrorWasntReady(t *testing.T) {
	h := newELHost(t, "almalinux", "9.6", "AlmaLinux")
	h.dnfFails = []string{dnfNoMirrorHadMetadata, dnfNoMirrorHadPackage}
	o := opts("")
	o.Yes = true
	out := &bytes.Buffer{}
	o.Out = out
	if _, err := Run(context.Background(), h.system(t), o, "test"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out.String(), "  • install Docker (docker-ce, docker-ce-cli, containerd.io)\n"+mirrorRetryLine+"  • ") || strings.Count(out.String(), "mirror") != 1 {
		t.Fatalf("the install must say once, under its step, that it tries again, and show none of dnf's errors:\n%s", out)
	}
	if m := manifestOf(t, h); !contains(m.PackagesInstalled, "docker-ce") {
		t.Fatalf("manifest: %+v", m)
	}
}
