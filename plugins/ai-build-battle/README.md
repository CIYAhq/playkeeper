# AI Build Battle plugin

The Paper plugin behind the AI Build Battle template: `/aibuild <prompt>` has an AI model build the prompt in front of the player, block by block, and `/aibattle <model A> <model B> <prompt>` has two build it side by side, with the build tools, rules and limits of the build battle videos, paid with the server owner's own OpenRouter key. It isn't published anywhere: it ships inside Playkeeper (`internal/addons/firstparty`), and the template installs it like any other add-on.

Built against Paper's API for 1.21.11 with Java 21, so it runs on Paper and Purpur from 1.21 to 26.x.

## Playing

| Command | Does |
|---|---|
| `/aibuild <what to build>` | Shows the model, this build's spend cap and today's spend, then builds after a click on **[Start]** |
| `/aibattle <model A> <model B> <what to build>` | Two models build it at once, A on the left and B on the right, then asks which one wins |
| `/aibuild stop` | Stops the build or battle; what's placed stays |
| `/aibuild status` | The build under way, today's spend, the caps and whether a key is set |
| `/aibuild model <id>` | Sets the model: any OpenRouter model id that can call tools, or a short name from `aliases` in `config.yml` (`claude`, `gpt`, `opus`, `luna`, `gemini`) |
| `/aibuild reload` | Reads `config.yml` again |

`aibuild.use` (operators by default) runs all of them. Everyone in the world sees the steps, the boss bar and the result.

- **The plot** is 61 by 61 blocks and 80 high by default, in front of the player, with its ground level with theirs. Its south side faces them, so the model's "south and east face the viewer" holds whichever way they looked. A build doesn't start where something is already standing.
- **Same as the videos:** the `build` tool (fill, hollow fill, place, remove, with block states as in `/setblock`) and `finish`, the videos' instructions, at most 10 build calls, 6,000 blocks and 45 minutes, high reasoning effort, and a picture of the plot from the south-east after each build call for models that take pictures. What differs is said plainly in the instructions: blocks are placed by the plugin, 20 a second, instead of by a bot player, and TNT isn't available.
- **Battles:** two plots side by side with a gap, each turned so the player sees it from its south-east, and each model with the same limits and its own boss bar. Each model gets the build cap; the daily cap is shared, and requests in flight count against it at their worst case, so two models answering at once can't pass it.
- **Spend caps:** before every request the plugin works out the most the answer may cost and gives the model only the output that still fits under both caps (`spend.per-build`, `spend.per-day`), as the videos' harness does. Costs come from OpenRouter's own report of each request. A build stops when the next request wouldn't fit. Today's spend is kept in `plugins/AIBuildBattle/spend.yml`; the day starts at midnight UTC.

## The key

On Playkeeper the key is set in the dashboard, on the server's Plugins tab, and kept outside the server's folder: the container sees it read-only at `/run/playkeeper/secrets/openrouter_api_key`. Anywhere else, set `OPENROUTER_API_KEY` in the server's environment. The plugin reads it at the start of every build and never writes it to chat, the console, the log or a file; error text from OpenRouter has anything key-shaped taken out before it's shown.

## Building

```bash
make plugins        # builds the jar (Java 21) and copies it to internal/addons/firstparty/ai-build-battle.jar
```

or `./gradlew build` here, which also runs the unit tests. The jar is reproducible: the same sources make the same bytes, so the copy Playkeeper ships can be checked against a fresh build. After changing the plugin, raise `version` in `build.gradle.kts`, run `make plugins`, and update the pin in `site/data/templates/ai-build-battle.json`.

## Tested

On Paper 26.2 build 129 (Java 25), offline mode on localhost, with a mineflayer test player and a real OpenRouter key: full builds with GPT-6 Luna (10 build calls, 1,356 and 4,738 blocks, $0.01 and $0.02) and GPT-6.1 Sol; battles of GPT-6 Luna against Xiaomi MiMo Flash (3,717 and 6,000 blocks) and against itself, to the end; the key read from `/run/playkeeper/secrets` with no environment variable; the build cap stopping a build; the daily cap refusing one; stop; a missing key and a key OpenRouter refuses. No key appeared in the server's log.
