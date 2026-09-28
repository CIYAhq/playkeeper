package site

import "strings"

// A MinecraftColor is one of the game's colour codes, as the colour codes
// tool lists it. js/tools/mc-text.js draws text in the same colours
// (mccolors_test.go keeps them in step). Java is empty for Bedrock's own.
// From minecraft.wiki's Formatting codes, 28 Sep 2026.
type MinecraftColor struct {
	Code, Name, ID, Java, Bedrock string
}

// Hex is its colour as minecraft.wiki writes it, like #AA0000: Java's, or
// Bedrock's for Bedrock's own.
func (c MinecraftColor) Hex() string { return strings.ToUpper(firstOf(c.Java, c.Bedrock)) }

// BedrockHex is Bedrock's colour when it differs from Java's.
func (c MinecraftColor) BedrockHex() string {
	if c.Java == "" || c.Java == c.Bedrock {
		return ""
	}
	return strings.ToUpper(c.Bedrock)
}

var minecraftColors = []MinecraftColor{
	{"0", "Black", "black", "#000000", "#000000"},
	{"1", "Dark blue", "dark_blue", "#0000aa", "#0000aa"},
	{"2", "Dark green", "dark_green", "#00aa00", "#00aa00"},
	{"3", "Dark aqua", "dark_aqua", "#00aaaa", "#00aaaa"},
	{"4", "Dark red", "dark_red", "#aa0000", "#aa0000"},
	{"5", "Dark purple", "dark_purple", "#aa00aa", "#aa00aa"},
	{"6", "Gold", "gold", "#ffaa00", "#ffaa00"},
	{"7", "Gray", "gray", "#aaaaaa", "#c5c5c5"},
	{"8", "Dark gray", "dark_gray", "#555555", "#545454"},
	{"9", "Blue", "blue", "#5555ff", "#447fff"},
	{"a", "Green", "green", "#55ff55", "#54ff54"},
	{"b", "Aqua", "aqua", "#55ffff", "#54ffff"},
	{"c", "Red", "red", "#ff5555", "#ff5454"},
	{"d", "Light purple", "light_purple", "#ff55ff", "#ff54ff"},
	{"e", "Yellow", "yellow", "#ffff55", "#ffff54"},
	{"f", "White", "white", "#ffffff", "#ffffff"},
}

// bedrockColors are Bedrock Edition's own; on Java, m and n are formats.
var bedrockColors = []MinecraftColor{
	{"g", "Minecoin gold", "minecoin_gold", "", "#efce16"},
	{"h", "Quartz", "material_quartz", "", "#d9ccb8"},
	{"i", "Iron", "material_iron", "", "#a9b4b7"},
	{"j", "Netherite", "material_netherite", "", "#8f727d"},
	{"m", "Redstone", "material_redstone", "", "#ee222c"},
	{"n", "Copper", "material_copper", "", "#c87363"},
	{"p", "Gold (material)", "material_gold", "", "#ffbf1e"},
	{"q", "Emerald", "material_emerald", "", "#13a045"},
	{"s", "Diamond", "material_diamond", "", "#5fecff"},
	{"t", "Lapis", "material_lapis", "", "#577bff"},
	{"u", "Amethyst", "material_amethyst", "", "#b66cdd"},
	{"v", "Resin", "material_resin", "", "#ff6a00"},
	{"w", "Party blue", "party_blue", "", "#8bb3ff"},
}

func init() {
	toolData["minecraftColors"] = func() any { return minecraftColors }
	toolData["bedrockColors"] = func() any { return bedrockColors }
}
