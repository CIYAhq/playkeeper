package io.playkeeper.aibuild;

import com.google.gson.JsonArray;
import com.google.gson.JsonParser;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

import java.io.File;
import java.nio.file.Files;
import java.nio.file.Path;
import java.time.Clock;
import java.time.Instant;
import java.time.ZoneOffset;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.logging.Logger;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

class MoneyAndKeysTest {
    @Test
    void theCapLeavesAMarginAndPricesTheInputHigh() {
        assertEquals(0.05, Budget.margin(1.00), 1e-9);
        assertEquals(0.20, Budget.margin(10.50), 1e-9);
        // $1 cap, $0.10 spent, $0.05 of input, $10 per million output tokens: $0.80 of room.
        long room = Budget.room(1.00, 0.10, 0.05, 0.00001);
        assertTrue(room >= 79_999 && room <= 80_000, "room " + room);
        assertTrue(Budget.room(0.10, 0.10, 0.01, 0.00001) < 0);
    }

    @Test
    void picturesCountAsManyTokens() {
        JsonArray messages = JsonParser.parseString("[{\"role\":\"system\",\"content\":\"" + "x".repeat(2500) + "\"},"
                + "{\"role\":\"user\",\"content\":[{\"type\":\"image_url\",\"image_url\":{\"url\":\"data:\"}},{\"type\":\"text\",\"text\":\"look\"}]}]").getAsJsonArray();
        assertEquals(1000 + 2 + 1600 + 4000, Budget.inputTokensHigh(messages));
    }

    @Test
    void theNextRequestIsEstimatedFromTheLastOnesCount() {
        JsonArray messages = JsonParser.parseString("[{\"role\":\"system\",\"content\":\"" + "x".repeat(25000) + "\"},"
                + "{\"role\":\"assistant\",\"content\":\"" + "y".repeat(25000) + "\"},"
                + "{\"role\":\"tool\",\"content\":\"" + "z".repeat(250) + "\"},"
                + "{\"role\":\"user\",\"content\":[{\"type\":\"image_url\",\"image_url\":{\"url\":\"data:\"}}]}]").getAsJsonArray();
        // 9,000 prompt tokens counted last time, 3,000 answered, then a tool report and a picture.
        assertEquals(9000 + 3000 + 100 + 1600 + 1000, Budget.nextInputTokensHigh(9000, 3000, messages, 1));
    }

    @Test
    void theDearestTierAppliesToLongPrompts() {
        OpenRouter.Model m = OpenRouter.parseModels(JsonParser.parseString(MODELS).getAsJsonObject()).get("openai/gpt-6.1-sol");
        assertEquals("GPT-6.1 Sol", m.name());
        assertTrue(m.tools() && m.pictures() && m.reasoning());
        assertEquals(0.00001, m.priceFor(1000).out(), 1e-12);
        assertEquals(0.000015, m.priceFor(300_000).out(), 1e-12);
        assertEquals(0.0000025, m.priceFor(1000).inputHigh(), 1e-12);
    }

    @Test
    void modelsWithoutAKnownPriceAreLeftOut() {
        Map<String, OpenRouter.Model> all = OpenRouter.parseModels(JsonParser.parseString(MODELS).getAsJsonObject());
        assertFalse(all.containsKey("typesafe/router"));
        assertFalse(all.get("some/text-only").pictures());
        assertFalse(all.get("some/text-only").tools());
    }

    @Test
    void theKeyFileComesBeforeTheEnvironment(@TempDir Path dir) throws Exception {
        Path file = dir.resolve("openrouter_api_key");
        assertEquals(Optional.of("sk-or-env"), Keys.read(file, "sk-or-env"));
        Files.writeString(file, "sk-or-file\n");
        assertEquals(Optional.of("sk-or-file"), Keys.read(file, "sk-or-env"));
        assertEquals(Optional.empty(), Keys.read(dir.resolve("none"), " "));
    }

    @Test
    void errorsNeverCarryAKey() {
        String key = "sk-or-v1-0123456789abcdef0123456789abcdef";
        String out = Keys.scrub("Invalid key " + key + " and sk-proj-abcdefghijkl\nsee docs", key);
        assertFalse(out.contains(key));
        assertFalse(out.contains("sk-proj-abcdefghijkl"));
        assertEquals("Invalid key [key] and [key] see docs", out);
    }

    @Test
    void theDayStartsAtMidnightUtcAndRequestsInFlightCount(@TempDir Path dir) {
        File f = dir.resolve("spend.yml").toFile();
        Ledger l = new Ledger(f, Clock.fixed(Instant.parse("2026-09-30T23:00:00Z"), ZoneOffset.UTC), Logger.getAnonymousLogger());
        l.reserve("a", 0.40);
        l.settle("b", 0.25);
        assertEquals(0.25, l.spentToday(), 1e-9);
        assertEquals(0.65, l.committedExcept("b"), 1e-9);
        assertEquals(0.25, l.committedExcept("a"), 1e-9);
        l.settleAllAtWorst();
        assertEquals(0.65, new Ledger(f, Clock.fixed(Instant.parse("2026-09-30T23:30:00Z"), ZoneOffset.UTC), Logger.getAnonymousLogger()).spentToday(), 1e-9);
        assertEquals(0, new Ledger(f, Clock.fixed(Instant.parse("2026-10-01T00:00:01Z"), ZoneOffset.UTC), Logger.getAnonymousLogger()).spentToday(), 1e-9);
    }

    @Test
    void thePromptStatesTheVideosLimits() {
        Settings s = new Settings("m", 1, 5, 10, 6000, 45, 30, 80, 20, true, "high", 48000, false, Map.of("claude", "anthropic/claude-sonnet-5.5"));
        assertEquals("anthropic/claude-sonnet-5.5", s.modelFor("Claude"));
        assertEquals("openai/gpt-6.1-sol", s.modelFor("openai/gpt-6.1-sol"));
        String p = Prompts.system("26.2", s, true);
        assertTrue(p.contains("x and z from -30 to 30, y from 0 to 80"));
        assertTrue(p.contains("at most 10 build calls, 6000 blocks placed in total, and 45 minutes in all"));
        assertTrue(p.contains("a picture of your plot"));
        assertTrue(p.contains("The camera films your plot from the south-east and a little above, so the south (+z) and east (+x) sides face the viewer."));
        assertFalse(Prompts.system("26.2", s, false).contains("a picture of your plot"));
        JsonArray tools = Prompts.tools();
        assertEquals(List.of("build", "finish"), List.of(name(tools, 0), name(tools, 1)));
    }

    private static String name(JsonArray tools, int i) {
        return tools.get(i).getAsJsonObject().getAsJsonObject("function").get("name").getAsString();
    }

    private static final String MODELS = """
            {"data": [
              {"id": "openai/gpt-6.1-sol", "name": "OpenAI: GPT-6.1 Sol",
               "architecture": {"input_modalities": ["file", "image", "text"]},
               "supported_parameters": ["tools", "reasoning", "max_tokens"],
               "top_provider": {"max_completion_tokens": 128000},
               "pricing": {"prompt": "0.000002", "completion": "0.00001", "input_cache_write": "0.0000025",
                 "overrides": [{"min_prompt_tokens": 272000, "prompt": "0.000004", "completion": "0.000015", "input_cache_write": "0.000005"}]}},
              {"id": "typesafe/router", "name": "Router", "pricing": {"prompt": "-1", "completion": "-1"}},
              {"id": "some/text-only", "name": "Some: Text", "architecture": {"input_modalities": ["text"]},
               "supported_parameters": [], "top_provider": {"max_completion_tokens": null},
               "pricing": {"prompt": "0.0000001", "completion": "0.0000002"}}
            ]}
            """;
}
