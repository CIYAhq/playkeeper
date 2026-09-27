package worldimport

import "strings"

// Guide explains how to get a world out of another host, or out of
// Minecraft itself, as a file Inspect reads. The UI offers the guides in a
// picker and shows the chosen one as numbered steps.
type Guide struct {
	ID   string `json:"id"`
	Name Text   `json:"name"`
	// HelpURL is the host's own help page the steps follow.
	HelpURL string `json:"helpUrl,omitempty"`
	// Download says what the download from the host contains.
	Download Text   `json:"download"`
	Steps    []Text `json:"steps"`
	Notes    []Note `json:"notes,omitempty"`
}

// Text is one text of a guide: Key and Params for the UI's translations,
// and the English Text with the parameters filled in.
type Text struct {
	Key    string            `json:"key"`
	Text   string            `json:"text"`
	Params map[string]string `json:"params,omitempty"`
}

// Note is a tip that goes with a guide's steps. Warning marks one that can
// cost progress or make the import fail.
type Note struct {
	Text
	Warning bool `json:"warning,omitempty"`
}

// Guides returns the guides in the order the UI offers them.
func Guides() []Guide {
	return []Guide{singleplayerGuide(), realmsGuide(), aternosGuide(), minehutGuide(), otherHostGuide()}
}

// GuideByID returns the guide with the given ID.
func GuideByID(id string) (Guide, bool) {
	for _, g := range Guides() {
		if g.ID == id {
			return g, true
		}
	}
	return Guide{}, false
}

// guideKeys builds the texts of one guide, keyed worldimport.guide.<id>.*.
// Params replace {name} placeholders in the English text.
type guideKeys string

func (k guideKeys) text(part, template string, kv ...string) Text {
	t := Text{Key: "worldimport.guide." + string(k) + "." + part, Text: template}
	if len(kv) > 0 {
		t.Params = make(map[string]string, len(kv)/2)
		for i := 0; i+1 < len(kv); i += 2 {
			t.Params[kv[i]] = kv[i+1]
			t.Text = strings.ReplaceAll(t.Text, "{"+kv[i]+"}", kv[i+1])
		}
	}
	return t
}

func (k guideKeys) step(id, template string, kv ...string) Text {
	return k.text("step."+id, template, kv...)
}

func (k guideKeys) tip(id, template string, kv ...string) Note {
	return Note{Text: k.text("note."+id, template, kv...)}
}

func (k guideKeys) warning(id, template string, kv ...string) Note {
	return Note{Text: k.text("note."+id, template, kv...), Warning: true}
}

// Steps from Mojang's help center, checked 2026-09-25:
// https://help.minecraft.net/hc/en-us/articles/40340122067085 (Make Backup),
// https://help.minecraft.net/hc/en-us/articles/4409159214605 (backups folder)
// and https://help.minecraft.net/hc/en-us/articles/360053272471 (saves
// folders).
func singleplayerGuide() Guide {
	k := guideKeys("singleplayer")
	return Guide{
		ID:       "singleplayer",
		Name:     k.text("name", "A singleplayer world"),
		HelpURL:  "https://help.minecraft.net/hc/en-us/articles/40340122067085-Save-a-Backup-of-Your-Minecraft-Java-Edition-World",
		Download: k.text("download", "A .zip file of the world that Minecraft makes itself, with the date in its name."),
		Steps: []Text{
			k.step("open", "Launch Minecraft: Java Edition and select Singleplayer."),
			k.step("edit", "Select the world, then select Edit."),
			k.step("backup", "Select Make Backup."),
			k.step("find", "Select Open Backups Folder on the Edit World screen. The backup is the .zip file with today's date in its name."),
			k.step("upload", "Upload that .zip file here."),
		},
		Notes: []Note{
			k.tip("saves", "You can also pack the world's folder into a .zip file yourself. Worlds are in {windows} on Windows and in {macos} on macOS.",
				"windows", `%appdata%\.minecraft\saves`, "macos", "~/Library/Application Support/minecraft/saves"),
			k.warning("version", "Give the server the Minecraft version you last played the world with, or a newer one. Minecraft can't load a world saved by a newer version."),
			k.warning("bedrock", "Worlds from Bedrock Edition, such as .mcworld files, can't be imported, because Java servers can't load them."),
		},
	}
}

// Steps from Mojang's help center, checked 2026-09-25:
// https://help.minecraft.net/hc/en-us/articles/37820630255245 (download),
// https://help.minecraft.net/hc/en-us/articles/28717462139149 (when Realms
// saves, and how long worlds and backups are kept) and
// https://help.minecraft.net/hc/en-us/articles/20712000178317 (Configure and
// the active world).
func realmsGuide() Guide {
	k := guideKeys("realms")
	return Guide{
		ID:       "realms",
		Name:     k.text("name", "Minecraft Realms"),
		HelpURL:  "https://help.minecraft.net/hc/en-us/articles/37820630255245-Download-a-Realms-world-in-Minecraft-Java-Edition",
		Download: k.text("download", "Minecraft downloads the Realm's world into your singleplayer worlds. A backup of it made in the game is the .zip file to upload."),
		Steps: []Text{
			k.step("open", "Launch Minecraft: Java Edition and select Minecraft Realms."),
			k.step("configure", "Select the Realm with the world you want, then select Configure."),
			k.step("download", "Select World Backups, then Download Latest, then Continue. Minecraft adds the world to your singleplayer worlds."),
			k.step("backup", "Go back to the main menu, select Singleplayer, select the downloaded world, then select Edit and Make Backup."),
			k.step("find", "Select Open Backups Folder. The backup is the .zip file with today's date in its name."),
			k.step("upload", "Upload that .zip file here."),
		},
		Notes: []Note{
			k.tip("owner", "Configure is the Realm owner's menu, so the owner has to download the world."),
			k.tip("slot", "If the Realm has several worlds, first select the one you want on the Worlds tab of Configure, so it is the active world."),
			k.tip("version", "The world is saved by the Minecraft version the Realm runs. Give the server that version or a newer one."),
			k.warning("latest", "Download Latest gets the Realm's latest save. Realms saves every 30 minutes and whenever a player uses Save & Quit or disconnects, so ask everyone to leave first."),
			k.warning("expired", "After a Realms subscription ends, its world can still be downloaded for 18 months, but its backups are deleted after 30 days."),
		},
	}
}

// Steps from Aternos' help center, checked 2026-09-25:
// https://support.aternos.org/hc/en-us/articles/360027235711 (download) and
// https://support.aternos.org/hc/en-us/articles/360028352811 (the Nether and
// End of Paper and Spigot worlds).
func aternosGuide() Guide {
	k := guideKeys("aternos")
	return Guide{
		ID:       "aternos",
		Name:     k.text("name", "Aternos"),
		HelpURL:  "https://support.aternos.org/hc/en-us/articles/360027235711-Download-your-world",
		Download: k.text("download", "One .zip file per world. On Paper and Spigot servers the Nether and the End are separate worlds, with names ending in _nether and _the_end."),
		Steps: []Text{
			k.step("worlds", "Sign in, choose your server and open the Worlds page: {url}", "url", "https://aternos.org/worlds/"),
			k.step("download", "Select your world and click Download. Aternos saves it as a .zip file."),
			k.step("dimensions", "If the server runs Paper or Spigot, download the worlds ending in _nether and _the_end the same way. They hold the Nether and the End."),
			k.step("upload", "Upload all the .zip files here together. Playkeeper puts the Nether and the End where this server expects them."),
		},
		Notes: []Note{
			k.warning("browser", "Download with your normal browser. Aternos asks you not to use download managers or other tools."),
			k.tip("merge", "Aternos' help explains how to merge the Nether and the End into the world by hand for singleplayer. You don't need to do that here."),
		},
	}
}

// Steps from Minehut's help center, checked 2026-09-25:
// https://support.minehut.com/hc/en-us/articles/27276033446163 (download),
// https://support.minehut.com/hc/en-us/articles/27082152888339 (world names),
// https://support.minehut.com/hc/en-us/articles/27126955782291 (SFTP) and
// https://support.minehut.com/hc/en-us/articles/53703725480851 (free servers
// marked for deletion).
func minehutGuide() Guide {
	k := guideKeys("minehut")
	return Guide{
		ID:       "minehut",
		Name:     k.text("name", "Minehut"),
		HelpURL:  "https://support.minehut.com/hc/en-us/articles/27276033446163-How-do-I-download-my-world",
		Download: k.text("download", "A .zip file with the files you selected in the File Manager."),
		Steps: []Text{
			k.step("activate", "Go to minehut.com and activate your server."),
			k.step("files", "Open the File Manager. It lists your worlds and other folders."),
			k.step("select", "Click your world's name, then select all the files of the world that appear on the right."),
			k.step("download", "Click Download at the bottom. A notification at the bottom of the page tells you when the download is complete."),
			k.step("dimensions", "If the list also has folders for the Nether and the End, such as world_nether and world_the_end, download them the same way."),
			k.step("upload", "Upload all the downloaded files here together."),
		},
		Notes: []Note{
			k.tip("names", "Minehut warns that spaces and special characters in a world's name cause problems when downloading it. Rename the world first if its name has any."),
			k.tip("sftp", "For big worlds, SFTP with a program such as FileZilla is easier, but Minehut offers it only on paid plans."),
			k.warning("deletion", "If Minehut has marked a free server for deletion, its files stay downloadable for 30 days after the email."),
		},
	}
}

func otherHostGuide() Guide {
	k := guideKeys("other")
	return Guide{
		ID:       "other",
		Name:     k.text("name", "Another host"),
		Download: k.text("download", "Usually a .zip or .tar.gz file of the folders you select in the host's file manager."),
		Steps: []Text{
			k.step("stop", "Stop the server in your host's panel, so the world is completely saved."),
			k.step("find", "Open the host's file manager and find the world folder. It is named after level-name in server.properties, usually world."),
			k.step("dimensions", "On Paper, Purpur or Spigot servers, also select the folders named like the world with _nether and _the_end added."),
			k.step("download", "Download them as a .zip or .tar.gz file. Downloading the whole server folder works too; Playkeeper finds the world in it."),
			k.step("upload", "Upload the file here. If the host gives you several files, upload them together."),
		},
		Notes: []Note{
			k.tip("ftp", "If the file manager fails on a big world, download the folders with an SFTP or FTP program such as FileZilla and pack them into a .zip file on your computer."),
			k.tip("formats", "Playkeeper reads .zip, .tar.gz and .tar files. Unpack .rar and .7z files on your computer and pack the world as a .zip file."),
		},
	}
}
