package addons

import (
	"fmt"

	"github.com/CIYAhq/playkeeper/internal/addons/fetch"
	"github.com/CIYAhq/playkeeper/internal/addons/firstparty"
)

// firstPartyProject is one of Playkeeper's own plugins, by project id, from
// the registry the binary carries.
func firstPartyProject(ref string) (*project, *Card, error) {
	fp := firstparty.Lookup(ref)
	if fp == nil {
		return nil, nil, firstPartyUnknown(ref)
	}
	card := &Card{
		Source: Playkeeper, ProjectID: fp.ID, Slug: fp.Slug, Name: fp.Name, Author: fp.Author, Summary: fp.Summary,
		License: fp.License, Updated: fp.Published,
	}
	return &project{Source: Playkeeper, ID: fp.ID, Slug: fp.Slug, Name: fp.Name, Summary: fp.Summary}, card, nil
}

// firstPartyCandidate is the one version of one of Playkeeper's own plugins:
// the build the binary carries, on every Minecraft version, for the server
// types the plugin runs on. It's nil for other types.
func firstPartyCandidate(p *project, t Target) (*candidate, error) {
	fp := firstparty.Lookup(p.ID)
	if fp == nil {
		return nil, firstPartyUnknown(p.ID)
	}
	if !fp.RunsOn(t.Type) {
		return nil, nil
	}
	j, err := fp.Jar()
	if err != nil {
		return nil, nil
	}
	return &candidate{
		project:   project{Source: Playkeeper, ID: fp.ID, Slug: fp.Slug, Name: fp.Name, Summary: fp.Summary},
		VersionID: j.Version, Number: j.Version, Channel: release, Published: fp.Published,
		FileName: j.FileName, HashAlgo: "sha256", Hash: j.SHA256, Size: j.Size, own: true,
	}, nil
}

func firstPartyUnknown(ref string) *Error {
	return fail(KindNotFound, kv("source", Playkeeper.Name(), "project", printable(ref)),
		fmt.Sprintf("Playkeeper has no plugin of its own called \"%s\".", printable(ref)),
		"Playkeeper's own plugins come with its templates.")
}

// stageFirstParty writes step s's jar from the binary into dir, checked
// against want as a download is.
func stageFirstParty(s Step, dir string, want fetch.Want) (string, error) {
	fp := firstparty.Lookup(s.ProjectID)
	if fp == nil {
		return "", firstPartyUnknown(s.ProjectID)
	}
	j, err := fp.Jar()
	if err != nil {
		return "", err
	}
	return fetch.Save(j.Open(), dir, want)
}
