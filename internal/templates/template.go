package templates

import (
	"github.com/CIYAhq/playkeeper/internal/addons"
)

const (
	// Format is the template format this package reads and writes. A
	// template of a newer format asks the user to update Playkeeper.
	Format = 1
	// Game is the only game Playkeeper runs so far.
	Game = "minecraft-java"
	// FileExtension ends template file names: survival.playkeeper-template.
	FileExtension = ".playkeeper-template"
	// ShareURL is the share page on playkeeper.io. Links carry the template
	// after #.
	ShareURL = "https://playkeeper.io/t"

	// MaxFileSize bounds a template file, and a link's template once
	// decompressed.
	MaxFileSize = 128 << 10
	// MaxLinkLength bounds the data after # in a link: about 120 add-ons
	// at pinned versions, or 250 as newest versions. Longer templates
	// travel as files.
	MaxLinkLength = 16 << 10
	// ChatLinkLength is about where chat apps start cutting links or
	// refusing the message (Discord allows 2,000 characters).
	ChatLinkLength = 2000

	MaxAddons    = 250
	MaxDataPacks = 32
)

const (
	maxFileLabel   = "128 KB" // MaxFileSize in messages
	maxName        = 32
	maxDescription = 280
	maxLabel       = 64 // add-on, modpack and pack names, version numbers
	maxMOTD        = 59
	maxPrompt      = 120
	maxURL         = 1024
	maxBuild       = 4
	minMemoryMB    = 512
	maxMemoryMB    = 65536
)

// Template is a server's setup as it can travel: what someone else needs to
// create the same server on their own Playkeeper, and nothing that
// identifies the server it came from.
type Template struct {
	// Format comes first, so a template file says what it is on its first
	// line.
	Format      int    `json:"playkeeperTemplate"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Author is left for a name the sharer chooses to show, once Playkeeper
	// has such names. Until then exports write none and imports show none:
	// a sign-in name is half of the login, and a file can say anything.
	Author string `json:"author,omitempty"`
	// Created is the day it was made, YYYY-MM-DD in UTC.
	Created string `json:"created,omitempty"`
	Game    string `json:"game"`
	Server  Server `json:"server"`
	// Settings left unset keep the new server's defaults.
	Settings Settings `json:"settings,omitzero"`
	// Addons are in install order: add-ons others need come before them.
	Addons  []Addon  `json:"addons,omitempty"`
	Modpack *Modpack `json:"modpack,omitempty"`
	Packs   []Pack   `json:"packs,omitempty"`
}

// Server is the server software.
type Server struct {
	// Type is an id from Playkeeper's registry of server types (paper,
	// purpur, fabric, quilt, neoforge, vanilla). Types added later travel
	// too; a Playkeeper that cannot create one says so when importing.
	Type             string `json:"type"`
	MinecraftVersion string `json:"minecraftVersion"`
	// Build holds the type's own build details, such as Paper's build or
	// Fabric's loader version, as short plain values. They are passed on
	// as they are: importing picks a version from its own catalog.
	Build map[string]string `json:"build,omitempty"`
}

// Settings are the choices someone makes when creating a server, with
// Playkeeper's names and ranges.
type Settings struct {
	Difficulty   string `json:"difficulty,omitempty"` // peaceful, easy, normal or hard
	PVP          *bool  `json:"pvp,omitempty"`
	GameMode     string `json:"gameMode,omitempty"` // survival, creative, adventure or spectator
	Hardcore     *bool  `json:"hardcore,omitempty"`
	ViewDistance int    `json:"viewDistance,omitempty"` // chunks, 3 to 32
	LevelType    string `json:"levelType,omitempty"`    // normal, flat, amplified or large_biomes
	MaxPlayers   int    `json:"maxPlayers,omitempty"`   // 1 to 100
	// MOTD is the server list message.
	MOTD      string `json:"motd,omitempty"`
	PlayStyle string `json:"playStyle,omitempty"` // friends, creative, hardcore or solo
	// MemoryMB is the memory the template's author suggests. The importing
	// machine decides what it can give.
	MemoryMB int `json:"memoryMB,omitempty"`
}

// Addon is a plugin or mod, by its source's ids.
type Addon struct {
	Source addons.Source `json:"source"`
	// Project is the source's project id (Hangar's numeric id as text).
	Project string `json:"project"`
	Slug    string `json:"slug,omitempty"`
	// Name is a label to show before anything is looked up. The source's
	// answer decides what is installed, and has to carry the same name.
	Name string `json:"name"`
	// Pin is the exact version, with the hash its source publishes for the
	// file. Without one, Latest is set: the newest version that fits the
	// new server.
	Pin    *Pin `json:"pin,omitempty"`
	Latest bool `json:"latest,omitempty"`
	// DependencyOf is the project (same source) this add-on is needed by;
	// empty when the template's author chose it.
	DependencyOf string `json:"dependencyOf,omitempty"`
}

func (a Addon) Key() addons.Key { return addons.Key{Source: a.Source, ProjectID: a.Project} }

// Pin is one published version of an add-on or modpack.
type Pin struct {
	VersionID     string `json:"versionId"`
	VersionNumber string `json:"versionNumber"`
	Channel       string `json:"channel,omitempty"` // release, beta or alpha
	// HashAlgo is sha512 for Modrinth and sha256 for Hangar: the hash each
	// publishes for its files.
	HashAlgo string `json:"hashAlgo"`
	Hash     string `json:"hash"`
}

// Modpack is the Modrinth modpack the server is built from. The pack
// decides the server's type and version and brings its own mods; Addons are
// what was added on top.
type Modpack struct {
	Source  addons.Source `json:"source"`
	Project string        `json:"project"`
	Slug    string        `json:"slug,omitempty"`
	Name    string        `json:"name"`
	// Pin is the pack version, with the SHA-512 of its .mrpack file.
	Pin Pin `json:"pin"`
}

const (
	ResourcePack = "resource"
	DataPack     = "data"
)

// Pack is a resource or data pack from a public HTTPS address.
type Pack struct {
	Kind string `json:"kind"` // resource or data
	// Name is a label, and the data pack's file name without .zip.
	Name string `json:"name"`
	URL  string `json:"url"`
	// SHA1 is what Minecraft clients check a resource pack against, so a
	// resource pack needs it. A data pack needs SHA256 or SHA1.
	SHA1   string `json:"sha1,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
	// Required and Prompt apply to the resource pack: whether players must
	// accept it, and the line their game shows when asking.
	Required bool   `json:"required,omitempty"`
	Prompt   string `json:"prompt,omitempty"`
}
