// Command release-sign makes and checks the signed release manifest that
// installed Playkeeper versions verify before installing an update. It is a
// maintainer and CI tool; it is not part of the release tarball.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/update"
)

const usage = `Usage:
  release-sign keygen   [--public-key-file internal/update/release.pub]
      Makes a release signing key: adds the public key to the key file and
      prints the private key on standard output, for example straight into
      the repository secret the release workflow signs with:
        go run ./cmd/release-sign keygen | gh secret set PLAYKEEPER_RELEASE_SIGNING_KEY --repo CIYAhq/playkeeper
  release-sign manifest --version V --tarball FILE [--changelog CHANGELOG.md | --notes TEXT] [--date RFC3339]
      Prints the release manifest for a tarball built by make package.
  release-sign sign     [--key-env PLAYKEEPER_RELEASE_SIGNING_KEY] FILE
      Writes FILE.sig with the private key from the environment variable.
  release-sign verify   [--public-key-file internal/update/release.pub] FILE
      Checks FILE.sig against the key file and prints the release version.
  release-sign pubkey   [--key-env PLAYKEEPER_RELEASE_SIGNING_KEY]
      Prints the public key line of the private key in the environment variable.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "keygen":
		err = keygen(os.Args[2:])
	case "manifest":
		err = manifest(os.Args[2:])
	case "sign":
		err = sign(os.Args[2:])
	case "verify":
		err = verify(os.Args[2:])
	case "pubkey":
		err = pubkey(os.Args[2:])
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "release-sign:", err)
		os.Exit(1)
	}
}

const defaultKeyFile = "internal/update/release.pub"

func keygen(args []string) error {
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	keyFile := fs.String("public-key-file", defaultKeyFile, "key file to add the public key to")
	fs.Parse(args)
	pub, priv, err := update.GenerateKey()
	if err != nil {
		return err
	}
	f, err := os.OpenFile(*keyFile, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(f, pub); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Println(priv)
	fmt.Fprintf(os.Stderr, "Added the public key to %s; commit that file. The private key went to standard output: keep it only in the PLAYKEEPER_RELEASE_SIGNING_KEY secret (and, if you like, a password manager).\n", *keyFile)
	return nil
}

func manifest(args []string) error {
	fs := flag.NewFlagSet("manifest", flag.ExitOnError)
	version := fs.String("version", "", "release version, without the leading v")
	tarball := fs.String("tarball", "", "playkeeper-linux-amd64.tar.gz built by make package")
	changelog := fs.String("changelog", "", "take the notes from this changelog's section for the version")
	notes := fs.String("notes", "", "the notes (instead of --changelog)")
	date := fs.String("date", time.Now().UTC().Format(time.RFC3339), "release date")
	fs.Parse(args)
	if *version == "" || *tarball == "" {
		return errors.New("--version and --tarball are required")
	}
	text := *notes
	if *changelog != "" {
		b, err := os.ReadFile(*changelog)
		if err != nil {
			return err
		}
		section, ok := update.ChangelogSection(b, *version)
		if !ok {
			return fmt.Errorf("%s has no \"## %s\" section with the release's changes", *changelog, *version)
		}
		text = section
	}
	if strings.TrimSpace(text) == "" {
		return errors.New("the release needs notes: pass --changelog or --notes")
	}
	b, err := update.BuildManifest(*version, *date, text, *tarball)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(b)
	return err
}

func privateKey(env string) (string, error) {
	s := strings.TrimSpace(os.Getenv(env))
	if s == "" {
		return "", fmt.Errorf("%s is empty; it must hold the release signing key (see CONTRIBUTING.md)", env)
	}
	return s, nil
}

func sign(args []string) error {
	fs := flag.NewFlagSet("sign", flag.ExitOnError)
	env := fs.String("key-env", "PLAYKEEPER_RELEASE_SIGNING_KEY", "environment variable with the private key")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return errors.New("sign needs the manifest file")
	}
	s, err := privateKey(*env)
	if err != nil {
		return err
	}
	priv, err := update.ParsePrivateKey(s)
	if err != nil {
		return err
	}
	b, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		return err
	}
	return os.WriteFile(fs.Arg(0)+".sig", update.Sign(priv, b), 0o644)
}

func verify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	keyFile := fs.String("public-key-file", defaultKeyFile, "trusted key file")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return errors.New("verify needs the manifest file")
	}
	kb, err := os.ReadFile(*keyFile)
	if err != nil {
		return err
	}
	keys, err := update.ParseKeys(kb)
	if err != nil {
		return err
	}
	b, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		return err
	}
	sig, err := os.ReadFile(fs.Arg(0) + ".sig")
	if err != nil {
		return err
	}
	m, err := update.VerifyManifest(b, sig, keys)
	if err != nil {
		return err
	}
	fmt.Printf("%s: release %s, signed by a key in %s\n", fs.Arg(0), m.Version, *keyFile)
	return nil
}

func pubkey(args []string) error {
	fs := flag.NewFlagSet("pubkey", flag.ExitOnError)
	env := fs.String("key-env", "PLAYKEEPER_RELEASE_SIGNING_KEY", "environment variable with the private key")
	fs.Parse(args)
	s, err := privateKey(*env)
	if err != nil {
		return err
	}
	priv, err := update.ParsePrivateKey(s)
	if err != nil {
		return err
	}
	fmt.Println(update.PublicLine(priv))
	return nil
}
