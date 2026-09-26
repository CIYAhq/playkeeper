package site

// A NavSection is one item of the header. Items are pages in the section in
// the order the menu lists them; pages not built yet are left out until
// they are, so adding a page is enough to put it in the menu.
type NavSection struct {
	Key, Label string
	// Link is where the item goes when it has no menu.
	Link string
	// Hub is the section's overview, shown last in its menu as "All …".
	Hub, HubLabel string
	Items         []string
}

var navSections = []NavSection{
	{Key: "features", Label: "Features", Hub: "/#features", HubLabel: "All features", Items: []string{
		"/features/mods-and-modpacks", "/features/free-address", "/features/backups", "/features/friends-and-team",
		"/features/crash-and-lag-help", "/features/worlds-and-map", "/features/automation", "/features/ai-agents",
	}},
	{Key: "guides", Label: "Guides", Hub: "/guides/host-a-minecraft-server", HubLabel: "All guides", Items: []string{
		"/guides/modded-minecraft-server", "/sizing", "/guides/play-minecraft-with-friends", "/guides/minecraft-server-cost",
	}},
	{Key: "compare", Label: "Compare", Hub: "/compare/minecraft-server-panels", HubLabel: "All panels compared", Items: []string{
		"/alternatives/aternos", "/alternatives/pterodactyl", "/alternatives/realms", "/alternatives/minehut",
	}},
	{Key: "docs", Label: "Docs", Link: "/docs"},
	{Key: "pricing", Label: "Pricing", Link: "/pricing"},
}

// A FooterLink goes to a page of the site (Path, shown once the page exists,
// or Else while it doesn't) or somewhere else (URL).
type FooterLink struct {
	Label, Path, Else, URL string
}

// FooterColumn is one of the footer's five groups.
type FooterColumn struct {
	Title string
	Links []FooterLink
}

func footerColumns(s Settings) []FooterColumn {
	return []FooterColumn{
		{"Product", []FooterLink{
			{Label: "Features", Path: "/#features"},
			{Label: "Templates", Path: "/templates", Else: "/#templates"},
			{Label: "Live demo", Path: "/demo/"},
			{Label: "Pricing", Path: "/pricing"},
			{Label: "Changelog", Path: "/changelog", Else: s.Repo + "/releases"},
		}},
		{"Guides", []FooterLink{
			{Label: "Host a server", Path: "/guides/host-a-minecraft-server"},
			{Label: "Modded server", Path: "/guides/modded-minecraft-server"},
			{Label: "Play with friends", Path: "/guides/play-minecraft-with-friends"},
			{Label: "Server cost", Path: "/guides/minecraft-server-cost"},
			{Label: "Sizing guide", Path: "/sizing"},
		}},
		{"Compare", []FooterLink{
			{Label: "Aternos alternative", Path: "/alternatives/aternos"},
			{Label: "Pterodactyl alternative", Path: "/alternatives/pterodactyl"},
			{Label: "Realms alternative", Path: "/alternatives/realms"},
			{Label: "Panels compared", Path: "/compare/minecraft-server-panels"},
		}},
		{"Resources", []FooterLink{
			{Label: "Docs", Path: "/docs"},
			{Label: "Blog", Path: "/blog"},
			{Label: "Free tools", Path: "/tools"},
			{Label: "Community", URL: s.Community.URL},
		}},
		{"Project", []FooterLink{
			{Label: "GitHub", URL: s.Repo},
			{Label: "About", Path: "/about"},
			{Label: "Brand", Path: "/brand"},
			{Label: "Privacy", Path: "/privacy"},
			{Label: "Logo credits", URL: s.Repo + "/blob/main/web/src/assets/logos/NOTICE.md"},
			{Label: "Licence: AGPL-3.0", URL: s.Repo + "/blob/main/LICENSE"},
		}},
	}
}
