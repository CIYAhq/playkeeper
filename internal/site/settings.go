package site

// Settings are the choices the site makes about the world outside it. Each is
// one switch: change it here and every page, link and header follows.
type Settings struct {
	// BaseURL is where the site is served, for canonical links, the sitemap
	// and social previews.
	BaseURL string
	// Repo is the GitHub repository.
	Repo string
	// Community is where "ask a question" links go.
	Community Community
	// InstallCommand is the one-line installer.
	InstallCommand string
	// StarsFrom is the star count the header starts showing next to Star on
	// GitHub; below it the button shows no number.
	StarsFrom int
	// Analytics counts visits on every page but the share page, whose
	// template must never leave the browser. The Content-Security-Policy
	// lets its script and collector in; empty turns it off. The live demo
	// loads the same script (web/src/demo/vite.ts).
	Analytics Analytics
	// Channels are where visitors come from, each with a code: /go/<code>
	// sends them to the landing page with the channel's UTM tags, and the
	// landing page then shows the channel's own install command,
	// /install/<code>, which the site's install log counts (nginx.conf).
	Channels []Channel
	// WhopPixel is the Whop business (biz_…) whose ad pixel /start, the Meta
	// ads' landing page, loads. No other page loads it, and only /start's
	// Content-Security-Policy lets it in; empty loads none.
	WhopPixel string
}

// Analytics is an OpenAnalytics site: the script pages load, the collector it
// reports to and the site's key.
type Analytics struct {
	Script, Collector, Key string
}

// Community is a place people can ask about Playkeeper.
type Community struct {
	URL string
	// Name is how links name it, as in "Ask on GitHub".
	Name string
	// Ask is the link text for asking a question there; In says where, in a
	// sentence: "Questions go in GitHub Discussions".
	Ask, In string
}

// Questions go to the repository's GitHub Discussions. Were it ever turned
// off, set Community to issues in Default: every link, its words and
// /community follow. The live demo's quiet prompt (web/src/demo) names
// GitHub Discussions too and links to /community.
var (
	discussions = Community{URL: repo + "/discussions", Name: "GitHub Discussions", Ask: "Ask in GitHub Discussions", In: "in GitHub Discussions"}
	issues      = Community{URL: repo + "/issues", Name: "GitHub", Ask: "Ask on GitHub", In: "on GitHub"}
	_           = issues
)

const repo = "https://github.com/CIYAhq/playkeeper"

// Default is playkeeper.io.
var Default = Settings{
	BaseURL:        "https://playkeeper.io",
	Repo:           repo,
	Community:      discussions,
	InstallCommand: "curl -fsSL https://playkeeper.io/install | sudo sh",
	StarsFrom:      50,
	Analytics: Analytics{
		Script:    "https://analytics-c.ciya.so/oa.js",
		Collector: "https://analytics-c.ciya.so",
		Key:       "oa_pk_tyJHnpyD4m-pl_XrUbi3maHu2Iqq87Uf",
	},
	Channels:  channels,
	WhopPixel: "biz_bbmk63HMB3yZ4c",
}
