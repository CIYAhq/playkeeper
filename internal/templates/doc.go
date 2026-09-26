// Package templates turns a server's setup into a template that someone else
// can use to create the same server on their own Playkeeper, as a file or a
// link, and plans the import of a template received from anyone.
//
// Every Playkeeper is self-hosted, so there is no central store: a template
// is self-contained data. It holds the server type and Minecraft version,
// the plain-language settings, plugins and mods by source, project and
// pinned version with the hash their source publishes (or, explicitly, "the
// newest version that fits"), an optional Modrinth modpack, and resource and
// data packs by public HTTPS address and checksum. It never holds ports,
// RCON, the allowlist, online mode, operators, secrets, world data or
// console commands.
//
// A template file is its JSON. A link is https://playkeeper.io/t#<data>,
// where <data> is base64url without padding of:
//
//	byte 0      link format, 1
//	bytes 1-2   length of the compressed template, big-endian
//	bytes 3-6   the first 4 bytes of the SHA-256 of the compressed template
//	bytes 7-    the template's canonical JSON, compressed with raw DEFLATE
//
// Browsers never send the part of an address after #, so a template in a
// link reaches no server on its way: the share page (site/t.html) reads it
// in the visitor's browser and hands it to their own dashboard the same way,
// as /servers/new#template=<data>.
//
// Everything read is untrusted. Decode checks the link's size, length and
// checksum, the decompressed size, nesting, counts, lengths and every field
// before PlanImport works out what this Playkeeper can create from it.
// Nothing is installed here: after the user confirms the plan, InstallAddon
// installs each add-on through the add-on library, which verifies every
// download against the hash its source publishes.
package templates
