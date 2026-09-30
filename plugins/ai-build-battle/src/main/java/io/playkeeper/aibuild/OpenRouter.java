package io.playkeeper.aibuild;

import com.google.gson.JsonArray;
import com.google.gson.JsonElement;
import com.google.gson.JsonObject;
import com.google.gson.JsonParser;

import java.io.IOException;
import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Set;

/**
 * OpenRouter's API: the public model list (prices, tools, pictures), the
 * key's own status (free) and chat completions. A request is retried only
 * when OpenRouter answered with an error, since failed generations aren't
 * billed; a connection lost mid-request may have been billed, so it's thrown
 * with {@code maybeBilled} for the spend cap to count.
 */
final class OpenRouter {
    private static final String BASE = "https://openrouter.ai/api/v1/";
    private static final Set<Integer> RETRY = Set.of(408, 409, 425, 429, 500, 502, 503, 504, 520, 521, 522, 523, 524, 525, 526, 527, 529);
    private static final Duration MODELS_FOR = Duration.ofHours(1);

    private final HttpClient http = HttpClient.newBuilder()
            .connectTimeout(Duration.ofSeconds(20))
            .followRedirects(HttpClient.Redirect.NEVER)
            .build();
    private Map<String, Model> models = Map.of();
    private long modelsAt;

    /** Per-token prices in US dollars; {@code minPromptTokens} is the prompt size a tier starts at. */
    record Price(long minPromptTokens, double in, double out, double cacheWrite) {
        double inputHigh() {
            return Math.max(in * 1.25, cacheWrite);
        }
    }

    record Model(String id, String name, boolean tools, boolean pictures, boolean reasoning, int maxOutput, List<Price> prices) {
        /** The dearest price that can apply to a prompt of this size. */
        Price priceFor(long promptTokens) {
            Price best = prices.get(0);
            for (Price p : prices) {
                if (promptTokens >= p.minPromptTokens() && p.out() >= best.out()) {
                    best = p;
                }
            }
            return best;
        }
    }

    record KeyStatus(boolean ok, int status, Double limitRemaining, String message) {
    }

    record Answer(JsonObject json, double errorCost) {
    }

    static final class ApiException extends Exception {
        final int status;
        final boolean maybeBilled;
        final double errorCost;

        ApiException(String message, int status, boolean maybeBilled, double errorCost) {
            super(message);
            this.status = status;
            this.maybeBilled = maybeBilled;
            this.errorCost = errorCost;
        }
    }

    /** A model by its OpenRouter id, from the public list (no key needed), cached for an hour. */
    synchronized Optional<Model> model(String id) throws IOException, InterruptedException {
        if (models.isEmpty() || System.currentTimeMillis() - modelsAt > MODELS_FOR.toMillis()) {
            HttpRequest req = HttpRequest.newBuilder(URI.create(BASE + "models")).timeout(Duration.ofSeconds(30)).GET().build();
            HttpResponse<String> res = http.send(req, HttpResponse.BodyHandlers.ofString(StandardCharsets.UTF_8));
            if (res.statusCode() != 200) {
                throw new IOException("OpenRouter's model list answered " + res.statusCode());
            }
            models = parseModels(JsonParser.parseString(res.body()).getAsJsonObject());
            modelsAt = System.currentTimeMillis();
        }
        return Optional.ofNullable(models.get(id));
    }

    static Map<String, Model> parseModels(JsonObject root) {
        Map<String, Model> out = new HashMap<>();
        for (JsonElement e : root.getAsJsonArray("data")) {
            JsonObject m = e.getAsJsonObject();
            try {
                String id = m.get("id").getAsString();
                String name = m.has("name") ? m.get("name").getAsString() : id;
                int colon = name.indexOf(": ");
                if (colon > 0) {
                    name = name.substring(colon + 2);
                }
                Set<String> params = new java.util.HashSet<>();
                if (m.has("supported_parameters")) {
                    for (JsonElement p : m.getAsJsonArray("supported_parameters")) {
                        params.add(p.getAsString());
                    }
                }
                boolean pictures = false;
                JsonObject arch = m.has("architecture") ? m.getAsJsonObject("architecture") : new JsonObject();
                if (arch.has("input_modalities")) {
                    for (JsonElement p : arch.getAsJsonArray("input_modalities")) {
                        pictures |= p.getAsString().equals("image");
                    }
                }
                int maxOutput = 0;
                if (m.has("top_provider") && m.getAsJsonObject("top_provider").has("max_completion_tokens")
                        && !m.getAsJsonObject("top_provider").get("max_completion_tokens").isJsonNull()) {
                    maxOutput = m.getAsJsonObject("top_provider").get("max_completion_tokens").getAsInt();
                }
                JsonObject pr = m.getAsJsonObject("pricing");
                List<Price> prices = new ArrayList<>();
                prices.add(price(pr, 0));
                if (pr.has("overrides")) {
                    for (JsonElement o : pr.getAsJsonArray("overrides")) {
                        JsonObject ov = o.getAsJsonObject();
                        prices.add(price(ov, ov.has("min_prompt_tokens") ? ov.get("min_prompt_tokens").getAsLong() : 0));
                    }
                }
                // Negative prices mark routers whose price is only known afterwards: no cap can hold for them.
                if (prices.stream().anyMatch(p -> p.in() < 0 || p.out() < 0)) {
                    continue;
                }
                out.put(id, new Model(id, name, params.contains("tools"), pictures, params.contains("reasoning"), maxOutput, List.copyOf(prices)));
            } catch (RuntimeException ignored) {
                // A model entry in a shape we don't know is left out.
            }
        }
        return out;
    }

    private static Price price(JsonObject p, long minPromptTokens) {
        double in = num(p, "prompt");
        return new Price(minPromptTokens, in, num(p, "completion"), p.has("input_cache_write") ? num(p, "input_cache_write") : in);
    }

    private static double num(JsonObject o, String k) {
        return o.has(k) && !o.get(k).isJsonNull() ? Double.parseDouble(o.get(k).getAsString()) : 0;
    }

    /** The key's own status: free, makes no completion. */
    KeyStatus key(String apiKey) {
        try {
            HttpRequest req = HttpRequest.newBuilder(URI.create(BASE + "key"))
                    .timeout(Duration.ofSeconds(20))
                    .header("Authorization", "Bearer " + apiKey)
                    .GET().build();
            HttpResponse<String> res = http.send(req, HttpResponse.BodyHandlers.ofString(StandardCharsets.UTF_8));
            if (res.statusCode() != 200) {
                return new KeyStatus(false, res.statusCode(), null, Keys.scrub(errorText(res.body()), apiKey));
            }
            JsonObject data = JsonParser.parseString(res.body()).getAsJsonObject().getAsJsonObject("data");
            Double left = data != null && data.has("limit_remaining") && !data.get("limit_remaining").isJsonNull()
                    ? data.get("limit_remaining").getAsDouble() : null;
            return new KeyStatus(true, 200, left, "");
        } catch (IOException e) {
            return new KeyStatus(false, 0, null, Keys.scrub(e.getMessage(), apiKey));
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            return new KeyStatus(false, 0, null, "interrupted");
        } catch (RuntimeException e) {
            return new KeyStatus(false, 0, null, "unreadable answer");
        }
    }

    /** One chat completion, retried on OpenRouter's error answers until {@code deadlineMillis}. */
    Answer complete(JsonObject body, String apiKey, long deadlineMillis) throws ApiException, InterruptedException {
        byte[] data = body.toString().getBytes(StandardCharsets.UTF_8);
        double errorCost = 0;
        ApiException last = null;
        for (int attempt = 1; attempt <= 4; attempt++) {
            long left = deadlineMillis - System.currentTimeMillis();
            HttpRequest req = HttpRequest.newBuilder(URI.create(BASE + "chat/completions"))
                    .timeout(Duration.ofMillis(Math.max(30_000, left + 60_000)))
                    .header("Authorization", "Bearer " + apiKey)
                    .header("Content-Type", "application/json")
                    .header("HTTP-Referer", "https://playkeeper.io/templates/ai-build-battle")
                    .header("X-Title", "Playkeeper AI Build Battle")
                    .POST(HttpRequest.BodyPublishers.ofByteArray(data))
                    .build();
            HttpResponse<String> res;
            try {
                res = http.send(req, HttpResponse.BodyHandlers.ofString(StandardCharsets.UTF_8));
            } catch (IOException e) {
                throw new ApiException(Keys.scrub("connection lost: " + e.getMessage(), apiKey), 0, true, errorCost);
            }
            JsonObject json;
            try {
                json = JsonParser.parseString(res.body()).getAsJsonObject();
            } catch (RuntimeException e) {
                json = new JsonObject();
            }
            JsonArray choices = json.has("choices") && json.get("choices").isJsonArray() ? json.getAsJsonArray("choices") : null;
            if (res.statusCode() == 200 && !json.has("error") && choices != null && !choices.isEmpty()) {
                return new Answer(json, errorCost);
            }
            if (json.has("usage") && json.getAsJsonObject("usage").has("cost")) {
                errorCost += json.getAsJsonObject("usage").get("cost").getAsDouble();
            }
            last = new ApiException(Keys.scrub("OpenRouter " + res.statusCode() + ": " + errorText(res.body()), apiKey), res.statusCode(), false, errorCost);
            if (res.statusCode() != 200 && !RETRY.contains(res.statusCode())) {
                throw last;
            }
            long wait = 4000L * attempt;
            if (System.currentTimeMillis() + wait > deadlineMillis) {
                throw last;
            }
            Thread.sleep(wait);
        }
        throw last;
    }

    private static String errorText(String body) {
        try {
            JsonObject o = JsonParser.parseString(body).getAsJsonObject();
            if (o.has("error") && o.get("error").isJsonObject() && o.getAsJsonObject("error").has("message")) {
                return o.getAsJsonObject("error").get("message").getAsString();
            }
        } catch (RuntimeException ignored) {
            // Not JSON: the body itself, cut short.
        }
        return body == null ? "" : body;
    }
}
