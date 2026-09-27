package minecraft

import (
	"strconv"
	"strings"
)

// NewestJava is the Java the newest Minecraft versions run on, and what a
// version Playkeeper can't read gets.
const NewestJava = 25

// runtimes are the runtime images, one per Java version a Minecraft release
// needs, pinned by digest (the multi-architecture index of each tag). They
// are the same release of the image, so they differ only in their Java.
var runtimes = []struct {
	java       int
	image, tag string
}{
	{25, Image, ImageTag},
	{21, "docker.io/itzg/minecraft-server@sha256:21b3d6bad32cc49ca15c8ceefcaffe94ac7fb6a53939fc7a5da066ec5dd0bc1d", "itzg/minecraft-server:2026.9.1-java21"},
	{17, "docker.io/itzg/minecraft-server@sha256:38afacde5d0cd46c255d744b582fd4b07524d43989da614f2bc9703c529d0fb9", "itzg/minecraft-server:2026.9.1-java17"},
	{16, "docker.io/itzg/minecraft-server@sha256:7a5a811a150bd2965cf0d62e5fd8693674d8ee797791b0dbba10f3d0772963a5", "itzg/minecraft-server:2026.9.1-java16"},
	{8, "docker.io/itzg/minecraft-server@sha256:aea37afb200728b004c41cbdc1b79e088955f32d22f5dac7f44cd94e5eaac0fe", "itzg/minecraft-server:2026.9.1-java8"},
}

// JavaFor is the Java version Mojang's launcher runs a Minecraft version on
// (javaVersion.majorVersion in the version's file): 8 up to 1.16.5, 16 for
// 1.17, 17 up to 1.20.4, 21 up to 1.21.11 and 25 from 26.1. Mods and their
// loaders are built for that Java: an older Fabric Loader can't read the
// classes of a newer one. A version it can't read, like a weekly snapshot,
// gets NewestJava.
func JavaFor(mc string) int {
	base, _, _ := strings.Cut(mc, "-")
	base, _, _ = strings.Cut(base, " ")
	var n [3]int
	parts := strings.Split(base, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return NewestJava
	}
	for i, p := range parts {
		v, err := strconv.Atoi(p)
		if err != nil || v < 0 {
			return NewestJava
		}
		n[i] = v
	}
	switch major, minor, patch := n[0], n[1], n[2]; {
	case major != 1:
		return NewestJava
	case minor <= 16:
		return 8
	case minor == 17:
		return 16
	case minor < 20 || minor == 20 && patch <= 4:
		return 17
	}
	return 21
}

// ImageFor is the pinned runtime image that runs Java java, with its tag
// for people to read; ok is false when Playkeeper has none.
func ImageFor(java int) (image, tag string, ok bool) {
	for _, r := range runtimes {
		if r.java == java {
			return r.image, r.tag, true
		}
	}
	return "", "", false
}

// Runtimes lists every pinned runtime image, newest Java first.
func Runtimes() []string {
	out := make([]string, 0, len(runtimes))
	for _, r := range runtimes {
		out = append(out, r.image)
	}
	return out
}
