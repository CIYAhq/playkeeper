package io.playkeeper.aibuild;

import com.google.gson.JsonArray;
import com.google.gson.JsonParser;

/**
 * The instructions and tools a model gets: the build battle videos' own,
 * with the parts about a bot player placing blocks by hand said the way
 * this plugin places them.
 */
final class Prompts {
    private Prompts() {
    }

    static String system(String minecraftVersion, Settings s, boolean pictures) {
        int half = s.half();
        return "You are building in Minecraft: Java Edition " + minecraftVersion + ", on a real server, while players watch. "
                + "Your blocks are placed one at a time, in the order you give them, so viewers watch your build appear: build from the ground up. "
                + "You can't run commands.\n\n"
                + "Your plot: x and z from -" + half + " to " + half + ", y from 0 to " + s.height() + ". "
                + "(0, 0, 0) is the centre of the plot at ground level: y = 0 is the first layer above the ground and y = -1 is the ground, which you may replace. "
                + "Players watch from the south (+z) side" + (pictures ? ", and the pictures you get are taken from the south-east and a little above," : " and a little to the east,")
                + " so the south (+z) and east (+x) sides face the viewer. Nobody else builds on your plot.\n\n"
                + "Use the build tool to place blocks, in order:\n"
                + "- {\"op\": \"fill\", \"from\": [x, y, z], \"to\": [x, y, z], \"block\": \"stone_bricks\"}: every block in the box, corners included\n"
                + "- the same with \"hollow\": true: only the box's outer shell (walls, floor and ceiling)\n"
                + "- {\"op\": \"place\", \"at\": [x, y, z], \"block\": \"oak_stairs[facing=south,half=bottom]\"}: one block\n"
                + "- {\"op\": \"remove\", \"from\": [x, y, z], \"to\": [x, y, z]}: break blocks\n"
                + "Blocks are Minecraft ids with optional block states, written as in /setblock. States are honoured as you write them: facing, half, type (slabs), axis, hanging (lanterns) and the rest. "
                + "Place only the lower half of a door and the foot of a bed; the other half is added for you. "
                + "Torches, lanterns, flowers, doors, ladders and carpets need a solid block to stand on or hang from. "
                + "Water, lava and technical blocks aren't available.\n\n"
                + "After each build call you get a report (what was placed, what failed and why)" + (pictures ? " and a picture of your plot from the camera" : "") + ". "
                + "Blocks appear about " + Math.round(s.blocksPerSecond()) + " a second.\n\n"
                + "Budget: at most " + s.buildCalls() + " build calls, " + s.blocks() + " blocks placed in total, and " + s.minutes()
                + " minutes in all, counting both your thinking and the building. Call finish when your build is complete.";
    }

    static String user(String challenge) {
        return "The challenge: " + challenge + "\nMake it as impressive as you can from the camera's view.";
    }

    static JsonArray tools() {
        return JsonParser.parseString(TOOLS).getAsJsonArray();
    }

    private static final String TOOLS = """
            [
              {"type": "function", "function": {
                "name": "build",
                "description": "Places and breaks these blocks in order, one at a time, while players watch. Returns a report of what happened.",
                "parameters": {
                  "type": "object",
                  "properties": {
                    "note": {"type": "string", "description": "A few words on what this step builds, shown to viewers. Example: \\"Outer walls and gatehouse\\"."},
                    "ops": {
                      "type": "array",
                      "description": "Operations, carried out in order.",
                      "items": {
                        "type": "object",
                        "properties": {
                          "op": {"type": "string", "enum": ["fill", "place", "remove"]},
                          "from": {"type": "array", "items": {"type": "integer"}, "description": "[x, y, z], for fill and remove"},
                          "to": {"type": "array", "items": {"type": "integer"}, "description": "[x, y, z], for fill and remove"},
                          "at": {"type": "array", "items": {"type": "integer"}, "description": "[x, y, z], for place"},
                          "block": {"type": "string", "description": "Block id with optional states, as in /setblock. Example: \\"spruce_stairs[facing=south,half=bottom]\\""},
                          "hollow": {"type": "boolean", "description": "fill only: just the outer shell of the box"}
                        },
                        "required": ["op"]
                      }
                    }
                  },
                  "required": ["note", "ops"]
                }
              }},
              {"type": "function", "function": {
                "name": "finish",
                "description": "Call once your build is complete. Ends your turn for good.",
                "parameters": {
                  "type": "object",
                  "properties": {"summary": {"type": "string", "description": "One sentence on what you built."}},
                  "required": ["summary"]
                }
              }}
            ]
            """;
}
