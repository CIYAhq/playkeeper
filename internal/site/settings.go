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
	// Waitlist is the address the pricing and blog email forms post to.
	// Empty keeps every email form off the site: nothing collects or sends
	// an email address, and the cards offer Watch releases on GitHub.
	Waitlist string
	// InstallCommand is the one-line installer.
	InstallCommand string
	// StarsFrom is the star count the header starts showing next to Star on
	// GitHub; below it the button shows no number.
	StarsFrom int
}

// Community is a place people can ask about Playkeeper.
type Community struct {
	URL string
	// Name is how links name it, as in "Ask on GitHub".
	Name string
	// Ask is the link text for asking a question there.
	Ask string
}

// GitHub Discussions is off on the repository, so questions go to its
// issues. When it's turned on, set Community to discussions in Default.
var (
	issues      = Community{URL: repo + "/issues", Name: "GitHub", Ask: "Ask on GitHub"}
	discussions = Community{URL: repo + "/discussions", Name: "GitHub Discussions", Ask: "Ask in GitHub Discussions"}
	_           = discussions
)

const repo = "https://github.com/CIYAhq/playkeeper"

// Default is playkeeper.io.
var Default = Settings{
	BaseURL:        "https://playkeeper.io",
	Repo:           repo,
	Community:      issues,
	Waitlist:       "",
	InstallCommand: "curl -fsSL https://playkeeper.io/install | sudo sh",
	StarsFrom:      50,
}
