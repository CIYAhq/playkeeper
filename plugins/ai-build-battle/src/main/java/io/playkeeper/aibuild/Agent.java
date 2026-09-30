package io.playkeeper.aibuild;

import com.google.gson.JsonArray;
import com.google.gson.JsonElement;
import com.google.gson.JsonObject;
import com.google.gson.JsonParser;

import java.util.Optional;
import java.util.concurrent.ExecutionException;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.Future;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.TimeoutException;

/**
 * One model's build loop, the videos' own: the model plans and calls build
 * with block operations, the plugin places them and reports back with a
 * picture, until the model calls finish or a limit is reached. Usage is
 * summed from what OpenRouter reports for each request, and the spend caps
 * are checked before every request.
 */
final class Agent {
    interface Hooks {
        boolean stopped();

        void thinking(int turn);

        void usage(Totals totals);

        void building(int step, String note);

        JsonObject build(JsonArray ops, String note, long deadline) throws Exception;

        void built(int step, JsonObject report);

        Optional<String> picture() throws Exception;

        void finished(String summary);

        void error(String message);
    }

    static final class Totals {
        int requests;
        long promptTokens, completionTokens, reasoningTokens, cachedTokens, cacheWriteTokens, totalTokens;
        /** What OpenRouter reported. */
        double cost;
        /** Worst-case charges for requests whose answers were lost: counted against the caps, not shown as spend. */
        double unaccounted;
    }

    record Result(String reason, String summary, Totals totals, int builds) {
    }

    private final OpenRouter api;
    private final OpenRouter.Model model;
    private final Settings settings;
    private final Ledger ledger;
    private final String ledgerKey;
    private final double capUSD;
    private final String apiKey;

    Agent(OpenRouter api, OpenRouter.Model model, Settings settings, Ledger ledger, String ledgerKey, double capUSD, String apiKey) {
        this.api = api;
        this.model = model;
        this.settings = settings;
        this.ledger = ledger;
        this.ledgerKey = ledgerKey;
        this.capUSD = capUSD;
        this.apiKey = apiKey;
    }

    Result run(String system, String prompt, long deadline, Hooks hooks) throws InterruptedException {
        ExecutorService calls = Executors.newSingleThreadExecutor(r -> {
            Thread t = new Thread(r, "AIBuild-request");
            t.setDaemon(true);
            return t;
        });
        try {
            return loop(system, prompt, deadline, hooks, calls);
        } finally {
            // A request still running is left to finish, so what it costs can be counted.
            calls.shutdown();
        }
    }

    private Result loop(String system, String prompt, long deadline, Hooks hooks, ExecutorService calls) throws InterruptedException {
        JsonArray messages = new JsonArray();
        messages.add(message("system", system));
        messages.add(message("user", prompt));
        Totals totals = new Totals();
        int builds = 0, idle = 0, lost = 0, placedTotal = 0;
        String finished = null, summary = null;
        while (finished == null) {
            if (hooks.stopped()) {
                finished = "stopped";
                break;
            }
            if (System.currentTimeMillis() > deadline) {
                finished = "time budget used up";
                break;
            }
            long tokensIn = Budget.inputTokensHigh(messages);
            OpenRouter.Price price = model.priceFor(tokensIn);
            double inputCost = tokensIn * price.inputHigh();
            long room = Budget.room(capUSD, totals.cost + totals.unaccounted, inputCost, price.out());
            long dayRoom = Budget.room(settings.perDayUSD(), ledger.committedExcept(ledgerKey), inputCost, price.out());
            long maxTokens = Math.min(settings.maxTokens(), Math.min(room, dayRoom));
            if (model.maxOutput() > 0) {
                maxTokens = Math.min(maxTokens, model.maxOutput());
            }
            if (maxTokens < Settings.MIN_OUTPUT_TOKENS) {
                finished = dayRoom < room ? "daily cap reached" : "spend cap reached";
                break;
            }
            double worst = inputCost + maxTokens * price.out();
            ledger.reserve(ledgerKey, worst);
            JsonObject body = new JsonObject();
            body.addProperty("model", model.id());
            body.add("messages", withCacheMarks(messages));
            body.add("tools", Prompts.tools());
            body.addProperty("tool_choice", "auto");
            JsonObject usage = new JsonObject();
            usage.addProperty("include", true);
            body.add("usage", usage);
            body.addProperty("max_tokens", maxTokens);
            if (model.reasoning() && !settings.reasoningEffort().isEmpty() && !settings.reasoningEffort().equals("none")) {
                JsonObject reasoning = new JsonObject();
                reasoning.addProperty("effort", settings.reasoningEffort());
                body.add("reasoning", reasoning);
            }
            hooks.thinking(totals.requests + 1);
            Future<OpenRouter.Answer> call = calls.submit(() -> api.complete(body, apiKey, deadline));
            OpenRouter.Answer answer = null;
            OpenRouter.ApiException failure = null;
            // Wait for the answer; a stop or the time limit ends the build, but an answer on its way is still counted.
            while (answer == null && failure == null) {
                try {
                    answer = call.get(1, TimeUnit.SECONDS);
                } catch (TimeoutException e) {
                    if (hooks.stopped()) {
                        // Stopping doesn't wait for the answer: its cost is counted when it arrives.
                        settleLater(call, worst, price);
                        return new Result("stopped", summary, totals, builds);
                    }
                    if (finished == null && System.currentTimeMillis() > deadline) {
                        finished = "time budget used up";
                    }
                    if (finished != null && System.currentTimeMillis() > deadline + 5 * 60_000L) {
                        call.cancel(true);
                        failure = new OpenRouter.ApiException("no answer", 0, true, 0);
                    }
                } catch (ExecutionException e) {
                    failure = e.getCause() instanceof OpenRouter.ApiException ae ? ae
                            : new OpenRouter.ApiException(Keys.scrub(String.valueOf(e.getCause().getMessage()), apiKey), 0, true, 0);
                }
            }
            if (failure != null) {
                double charge = failure.maybeBilled ? worst : 0;
                totals.cost += failure.errorCost;
                totals.unaccounted += charge;
                ledger.settle(ledgerKey, failure.errorCost + charge);
                hooks.error(failure.getMessage());
                if (finished != null) {
                    break;
                }
                if (failure.maybeBilled && ++lost <= 2) {
                    continue;
                }
                finished = "API error: " + failure.getMessage();
                break;
            }
            JsonObject json = answer.json();
            JsonObject u = json.has("usage") && json.get("usage").isJsonObject() ? json.getAsJsonObject("usage") : new JsonObject();
            long promptTokens = longOf(u, "prompt_tokens"), completionTokens = longOf(u, "completion_tokens");
            totals.requests++;
            totals.promptTokens += promptTokens;
            totals.completionTokens += completionTokens;
            totals.reasoningTokens += nested(u, "completion_tokens_details", "reasoning_tokens");
            totals.cachedTokens += nested(u, "prompt_tokens_details", "cached_tokens");
            totals.cacheWriteTokens += nested(u, "prompt_tokens_details", "cache_write_tokens");
            totals.totalTokens += u.has("total_tokens") ? longOf(u, "total_tokens") : promptTokens + completionTokens;
            // OpenRouter reports each request's cost; without it, list prices for every token keep the cap.
            double charged = (u.has("cost") && !u.get("cost").isJsonNull() ? u.get("cost").getAsDouble()
                    : promptTokens * price.in() + completionTokens * price.out()) + answer.errorCost();
            totals.cost += charged;
            ledger.settle(ledgerKey, charged);
            hooks.usage(totals);
            if (finished != null) {
                break;
            }
            JsonObject choice = json.getAsJsonArray("choices").get(0).getAsJsonObject();
            JsonObject msg = choice.has("message") && choice.get("message").isJsonObject() ? choice.getAsJsonObject("message") : new JsonObject();
            JsonObject assistant = new JsonObject();
            assistant.addProperty("role", "assistant");
            assistant.addProperty("content", msg.has("content") && msg.get("content").isJsonPrimitive() ? msg.get("content").getAsString() : "");
            JsonArray toolCalls = msg.has("tool_calls") && msg.get("tool_calls").isJsonArray() ? msg.getAsJsonArray("tool_calls") : new JsonArray();
            if (!toolCalls.isEmpty()) {
                assistant.add("tool_calls", toolCalls);
            }
            if (msg.has("reasoning_details") && !msg.get("reasoning_details").isJsonNull()) {
                assistant.add("reasoning_details", msg.get("reasoning_details"));
            }
            messages.add(assistant);
            if (toolCalls.isEmpty()) {
                idle++;
                if (idle >= 2 || totals.requests >= settings.buildCalls() + 4) {
                    finished = "stopped without calling finish";
                    break;
                }
                messages.add(message("user", "Please continue by calling build, or finish when your build is complete."));
                continue;
            }
            idle = 0;
            boolean viewNeeded = false;
            for (JsonElement el : toolCalls) {
                JsonObject tc = el.getAsJsonObject();
                String id = tc.has("id") ? tc.get("id").getAsString() : "";
                JsonObject fn = tc.has("function") ? tc.getAsJsonObject("function") : new JsonObject();
                String name = fn.has("name") ? fn.get("name").getAsString() : "";
                JsonObject args;
                try {
                    String raw = fn.has("arguments") ? fn.get("arguments").getAsString() : "{}";
                    args = JsonParser.parseString(raw.isBlank() ? "{}" : raw).getAsJsonObject();
                } catch (RuntimeException e) {
                    messages.add(toolResult(id, error("Your arguments weren't valid JSON. Nothing was built.")));
                    continue;
                }
                if (name.equals("finish")) {
                    finished = "called finish";
                    summary = args.has("summary") ? cut(args.get("summary").getAsString(), 300) : "";
                    JsonObject ok = new JsonObject();
                    ok.addProperty("ok", true);
                    messages.add(toolResult(id, ok));
                    hooks.finished(summary);
                    break;
                }
                if (!name.equals("build")) {
                    messages.add(toolResult(id, error("There is no tool called " + Ops.clip(name) + ".")));
                    continue;
                }
                if (builds >= settings.buildCalls()) {
                    messages.add(toolResult(id, error("No build calls left. Call finish.")));
                    continue;
                }
                builds++;
                String note = args.has("note") ? cut(args.get("note").getAsString(), 80) : "";
                JsonArray ops = args.has("ops") && args.get("ops").isJsonArray() ? args.getAsJsonArray("ops") : new JsonArray();
                hooks.building(builds, note);
                JsonObject report;
                try {
                    report = hooks.build(ops, note, deadline);
                } catch (InterruptedException e) {
                    throw e;
                } catch (Exception e) {
                    report = error("The build step couldn't run: " + Ops.clip(e.getMessage()));
                }
                placedTotal = report.has("totalPlaced") ? report.get("totalPlaced").getAsInt() : placedTotal;
                int blocksLeft = Math.max(0, settings.blocks() - placedTotal);
                double minutesLeft = Math.max(0, (deadline - System.currentTimeMillis()) / 60000.0);
                report.addProperty("buildCallsLeft", settings.buildCalls() - builds);
                report.addProperty("blocksLeft", blocksLeft);
                report.addProperty("minutesLeft", Math.round(minutesLeft * 10) / 10.0);
                hooks.built(builds, report);
                messages.add(toolResult(id, report));
                viewNeeded = true;
                if (blocksLeft == 0 || minutesLeft <= 0) {
                    finished = blocksLeft == 0 ? "block budget used up" : "time budget used up";
                }
                if (hooks.stopped()) {
                    finished = "stopped";
                }
            }
            if (finished != null) {
                break;
            }
            if (viewNeeded && model.pictures()) {
                Optional<String> picture;
                try {
                    picture = hooks.picture();
                } catch (InterruptedException e) {
                    throw e;
                } catch (Exception e) {
                    picture = Optional.empty();
                }
                String text = builds >= settings.buildCalls()
                        ? "Your plot from the camera now. You have no build calls left: call finish."
                        : "Your plot from the camera now.";
                if (picture.isPresent()) {
                    JsonArray content = new JsonArray();
                    JsonObject img = new JsonObject();
                    img.addProperty("type", "image_url");
                    JsonObject url = new JsonObject();
                    url.addProperty("url", picture.get());
                    img.add("image_url", url);
                    content.add(img);
                    JsonObject t = new JsonObject();
                    t.addProperty("type", "text");
                    t.addProperty("text", text);
                    content.add(t);
                    JsonObject user = new JsonObject();
                    user.addProperty("role", "user");
                    user.add("content", content);
                    messages.add(user);
                }
            }
        }
        return new Result(finished, summary, totals, builds);
    }

    private void settleLater(Future<OpenRouter.Answer> call, double worst, OpenRouter.Price price) {
        Thread t = new Thread(() -> {
            double charged = worst;
            try {
                JsonObject json = call.get(15, TimeUnit.MINUTES).json();
                JsonObject u = json.has("usage") && json.get("usage").isJsonObject() ? json.getAsJsonObject("usage") : new JsonObject();
                charged = u.has("cost") && !u.get("cost").isJsonNull() ? u.get("cost").getAsDouble()
                        : longOf(u, "prompt_tokens") * price.in() + longOf(u, "completion_tokens") * price.out();
            } catch (ExecutionException e) {
                if (e.getCause() instanceof OpenRouter.ApiException ae) {
                    charged = ae.errorCost + (ae.maybeBilled ? worst : 0);
                }
            } catch (Throwable e) {
                // Unknown: the worst case stays counted.
            }
            ledger.settle(ledgerKey, charged);
        }, "AIBuild-settle");
        t.setDaemon(true);
        t.start();
    }

    /** Anthropic caches only up to marked blocks: mark the system prompt and the newest user message. */
    private JsonArray withCacheMarks(JsonArray messages) {
        if (!model.id().startsWith("anthropic/")) {
            return messages;
        }
        JsonArray out = messages.deepCopy();
        mark(out.get(0).getAsJsonObject());
        for (int i = out.size() - 1; i > 0; i--) {
            JsonObject m = out.get(i).getAsJsonObject();
            if (m.get("role").getAsString().equals("user")) {
                mark(m);
                break;
            }
        }
        return out;
    }

    private static void mark(JsonObject m) {
        JsonObject cache = new JsonObject();
        cache.addProperty("type", "ephemeral");
        JsonElement c = m.get("content");
        if (c != null && c.isJsonPrimitive()) {
            JsonObject part = new JsonObject();
            part.addProperty("type", "text");
            part.addProperty("text", c.getAsString());
            part.add("cache_control", cache);
            JsonArray parts = new JsonArray();
            parts.add(part);
            m.add("content", parts);
        } else if (c != null && c.isJsonArray() && !c.getAsJsonArray().isEmpty()) {
            JsonArray parts = c.getAsJsonArray();
            parts.get(parts.size() - 1).getAsJsonObject().add("cache_control", cache);
        }
    }

    private static JsonObject message(String role, String content) {
        JsonObject m = new JsonObject();
        m.addProperty("role", role);
        m.addProperty("content", content);
        return m;
    }

    private static JsonObject toolResult(String id, JsonObject result) {
        JsonObject m = new JsonObject();
        m.addProperty("role", "tool");
        m.addProperty("tool_call_id", id);
        m.addProperty("content", result.toString());
        return m;
    }

    private static JsonObject error(String text) {
        JsonObject e = new JsonObject();
        e.addProperty("error", text);
        return e;
    }

    private static long longOf(JsonObject o, String k) {
        return o.has(k) && o.get(k).isJsonPrimitive() ? o.get(k).getAsLong() : 0;
    }

    private static long nested(JsonObject o, String a, String b) {
        return o.has(a) && o.get(a).isJsonObject() ? longOf(o.getAsJsonObject(a), b) : 0;
    }

    private static String cut(String s, int n) {
        String t = s == null ? "" : s.replaceAll("\\s+", " ").trim();
        return t.length() > n ? t.substring(0, n) : t;
    }
}
