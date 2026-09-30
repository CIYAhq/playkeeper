package io.playkeeper.aibuild;

import com.google.gson.JsonArray;
import com.google.gson.JsonElement;
import com.google.gson.JsonObject;

/**
 * The hard spend cap, as the videos enforce it: before each request, its
 * output limit is whatever still fits under the cap after a deliberately
 * high estimate of its input cost.
 */
final class Budget {
    private Budget() {
    }

    /** Kept free under every cap, for estimates that come in low: 5% of the cap, at most 20 cents. */
    static double margin(double capUSD) {
        return Math.min(0.20, capUSD * 0.05);
    }

    /** Output tokens that still fit under {@code capUSD}; negative when nothing does. */
    static long room(double capUSD, double spentUSD, double inputCostUSD, double outPerToken) {
        if (outPerToken <= 0) {
            return Long.MAX_VALUE;
        }
        return (long) Math.floor((capUSD - margin(capUSD) - spentUSD - inputCostUSD) / outPerToken);
    }

    /**
     * A high estimate of a request's input tokens: 2.5 characters a token for
     * text, 1,600 tokens a picture, and 4,000 for the tools and overhead.
     */
    static long inputTokensHigh(JsonArray messages) {
        long chars = 0;
        long pictures = 0;
        for (JsonElement e : messages) {
            JsonObject m = e.getAsJsonObject();
            JsonElement content = m.get("content");
            if (content != null && content.isJsonPrimitive()) {
                chars += content.getAsString().length();
            } else if (content != null && content.isJsonArray()) {
                for (JsonElement p : content.getAsJsonArray()) {
                    JsonObject part = p.getAsJsonObject();
                    if (part.has("image_url")) {
                        pictures++;
                    } else if (part.has("text")) {
                        chars += part.get("text").getAsString().length();
                    }
                }
            }
            if (m.has("tool_calls")) {
                chars += m.get("tool_calls").toString().length();
            }
            if (m.has("reasoning_details")) {
                chars += m.get("reasoning_details").toString().length();
            }
        }
        return (long) Math.ceil(chars / 2.5) + pictures * 1600 + 4000;
    }

    /** What the cheapest possible request costs at most: the input estimate plus the fewest output tokens. */
    static double smallestRequestUSD(OpenRouter.Model model, long promptTokens) {
        OpenRouter.Price p = model.priceFor(promptTokens);
        return promptTokens * p.inputHigh() + Settings.MIN_OUTPUT_TOKENS * p.out();
    }
}
