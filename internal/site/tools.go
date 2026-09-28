package site

// A Tool is a free tool on /tools and the search it's made for, which its
// title and heading must answer (tools_test.go). Tools not built yet are left
// out of the menu, the hub and Keep reading until their page exists.
type Tool struct {
	Path, Keyword string
}

var tools = []Tool{
	{"/tools/server-icon", "minecraft server icon"},
	{"/tools/color-codes", "minecraft color codes"},
	{"/tools/motd", "minecraft motd generator"},
	{"/tools/jvm-flags", "minecraft jvm arguments"},
	{"/tools/server-properties", "minecraft server properties"},
}

// toolData is what a tool's page shows from Go, by name, for the templates'
// data function: each tool registers its own in its own file's init.
var toolData = map[string]func() any{}

func toolPaths() []string {
	out := make([]string, len(tools))
	for i, t := range tools {
		out[i] = t.Path
	}
	return out
}

// A Swatch is a colour of the palette the tools' swatches offer. css/tools.css
// draws each with its class, c-<ID>, in the same colour.
type Swatch struct {
	ID, Name, Hex string
}

var palette = []Swatch{
	{"grass", "Grass green", "#3f9b3a"},
	{"forest", "Forest green", "#1f5f2c"},
	{"lime", "Lime", "#8cc63f"},
	{"sky", "Sky blue", "#3b8fd9"},
	{"ocean", "Ocean blue", "#1f4e9c"},
	{"night", "Night blue", "#1c2340"},
	{"violet", "Violet", "#7b44c9"},
	{"rose", "Rose", "#d6508f"},
	{"crimson", "Crimson", "#c23a32"},
	{"amber", "Amber", "#e8871e"},
	{"gold", "Gold", "#f2c230"},
	{"stone", "Stone grey", "#7d7f7a"},
	{"charcoal", "Charcoal", "#262825"},
	{"snow", "Snow", "#f4f4f0"},
	{"white", "White", "#ffffff"},
	{"black", "Black", "#151515"},
}
