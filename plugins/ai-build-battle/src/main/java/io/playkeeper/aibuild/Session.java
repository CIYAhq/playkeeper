package io.playkeeper.aibuild;

import com.google.gson.JsonArray;
import com.google.gson.JsonObject;
import net.kyori.adventure.bossbar.BossBar;
import net.kyori.adventure.text.Component;
import net.kyori.adventure.text.format.NamedTextColor;
import org.bukkit.Bukkit;
import org.bukkit.Chunk;
import org.bukkit.World;
import org.bukkit.entity.Player;
import org.bukkit.scheduler.BukkitTask;

import java.util.HashSet;
import java.util.Optional;
import java.util.Set;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicInteger;

/** One running build: the model's loop on its own thread, the blocks and the boss bar on the main thread. */
final class Session {
    private static final AtomicInteger IDS = new AtomicInteger();

    private final AIBuildBattlePlugin plugin;
    private final World world;
    private final Frame frame;
    private final OpenRouter.Model model;
    private final String prompt;
    private final String requester;
    private final double capUSD;
    private final Settings settings;
    private final String id = "build-" + IDS.incrementAndGet();
    private final BossBar bar;
    private final Set<Chunk> tickets = new HashSet<>();
    private volatile boolean stopped;
    private volatile String stoppedBy;
    private volatile Placer placer;
    private volatile int placedTotal;
    private volatile int step;
    private volatile String phase = "Starting";
    private volatile double spent;
    private long startedAt;
    private long deadline;
    private BukkitTask barTask;
    private volatile String result;

    /** "A" or "B" in a battle, null for one build. */
    private final String side;

    Session(AIBuildBattlePlugin plugin, World world, Frame frame, OpenRouter.Model model, String prompt, String requester, double capUSD, Settings settings, String side) {
        this.plugin = plugin;
        this.world = world;
        this.frame = frame;
        this.model = model;
        this.prompt = prompt;
        this.requester = requester;
        this.capUSD = capUSD;
        this.settings = settings;
        this.side = side;
        this.bar = BossBar.bossBar(Component.text(model.name()), 0f, "B".equals(side) ? BossBar.Color.BLUE : BossBar.Color.GREEN, BossBar.Overlay.PROGRESS);
    }

    String modelName() {
        return model.name();
    }

    String modelId() {
        return model.id();
    }

    World world() {
        return world;
    }

    /** The model's name, with its side in a battle: "A · Claude Sonnet 5.5". */
    String who() {
        return side == null ? model.name() : side + " · " + model.name();
    }

    String status() {
        if (result != null) {
            return who() + " is done: " + result + ".";
        }
        return who() + " is building \"" + prompt + "\": step " + step + " of " + settings.buildCalls() + ", "
                + Text.count(placedTotal) + " blocks, " + Text.clock(System.currentTimeMillis() - startedAt) + ", " + Text.money(spent) + " of " + Text.money(capUSD) + ".";
    }

    /** How it went, once it ended: "4 steps, 3,678 blocks, 12:34, $0.30". */
    String result() {
        return result;
    }

    /** Main thread. */
    void start(String apiKey) {
        startedAt = System.currentTimeMillis();
        deadline = startedAt + settings.minutes() * 60_000L;
        for (int x = -frame.half(); x <= frame.half() + 16; x += 16) {
            for (int z = -frame.half(); z <= frame.half() + 16; z += 16) {
                int cx = Math.max(-frame.half(), Math.min(frame.half(), x)), cz = Math.max(-frame.half(), Math.min(frame.half(), z));
                Chunk c = world.getChunkAt(frame.worldX(cx, cz) >> 4, frame.worldZ(cx, cz) >> 4);
                if (c.addPluginChunkTicket(plugin)) {
                    tickets.add(c);
                }
            }
        }
        barTask = Bukkit.getScheduler().runTaskTimer(plugin, this::refreshBar, 0, 20);
        if (side == null) {
            plugin.announce(world, Component.text(requester + " asked ", NamedTextColor.GRAY)
                    .append(Component.text(model.name(), NamedTextColor.GREEN))
                    .append(Component.text(" to build \"" + prompt + "\".", NamedTextColor.GRAY)), false);
        }
        String system = Prompts.system(Bukkit.getMinecraftVersion(), settings, model.pictures());
        String user = Prompts.user(prompt);
        Agent agent = new Agent(plugin.api(), model, settings, plugin.ledger(), id, capUSD, apiKey);
        Thread t = new Thread(() -> {
            Agent.Result result = null;
            try {
                result = agent.run(system, user, deadline, hooks());
            } catch (InterruptedException e) {
                Thread.currentThread().interrupt();
            } catch (RuntimeException e) {
                plugin.getLogger().warning("An AI build failed: " + Keys.scrub(e.toString(), apiKey));
            }
            Agent.Result r = result;
            if (plugin.isEnabled()) {
                Bukkit.getScheduler().runTask(plugin, () -> end(r));
            }
        }, "AIBuild-" + id);
        t.setDaemon(true);
        t.start();
    }

    /** False when it was already stopping. */
    boolean stop(String by) {
        if (stopped) {
            return false;
        }
        stoppedBy = by;
        stopped = true;
        return true;
    }

    /** The server is stopping: nothing more is placed, and the model's thread ends on its own. */
    void shutdown() {
        stop("the server");
        Placer p = placer;
        if (p != null) {
            p.cancel();
        }
        cleanUp();
    }

    private Agent.Hooks hooks() {
        return new Agent.Hooks() {
            @Override
            public boolean stopped() {
                return stopped || !plugin.isEnabled();
            }

            @Override
            public void thinking(int turn) {
                phase = "Thinking";
            }

            @Override
            public void usage(Agent.Totals totals) {
                spent = totals.cost;
            }

            @Override
            public void building(int n, String note) {
                step = n;
                phase = note.isEmpty() ? "Building" : note;
                String line = "Step " + n + " of " + settings.buildCalls() + (note.isEmpty() ? "" : ": " + note);
                sync(() -> plugin.announce(world, side == null ? Component.text(line, NamedTextColor.GRAY)
                        : Component.text(who() + "  ", colour()).append(Component.text(line, NamedTextColor.GRAY)), false));
            }

            @Override
            public JsonObject build(JsonArray ops, String note, long until) throws Exception {
                Ops.Plan plan = Ops.plan(ops, frame);
                Placer p = new Placer(plugin, world, frame, plan, settings.blocksPerSecond(), until, settings.blocks(), placedTotal,
                        () -> stopped || !plugin.isEnabled(), n -> placedTotal = n);
                placer = p;
                CompletableFuture<JsonObject> done = p.start();
                while (true) {
                    try {
                        return done.get(1, TimeUnit.SECONDS);
                    } catch (java.util.concurrent.TimeoutException e) {
                        if (!plugin.isEnabled()) {
                            p.cancel();
                        }
                    }
                }
            }

            @Override
            public void built(int n, JsonObject report) {
                phase = "Looking at it";
            }

            @Override
            public Optional<String> picture() throws Exception {
                CompletableFuture<java.util.Map<Long, org.bukkit.ChunkSnapshot>> snap = new CompletableFuture<>();
                sync(() -> {
                    try {
                        snap.complete(Picture.capture(world, frame));
                    } catch (RuntimeException e) {
                        snap.completeExceptionally(e);
                    }
                });
                String png = Picture.render(snap.get(30, TimeUnit.SECONDS), frame, world.getMinHeight(), world.getMaxHeight());
                if (settings.savePictures()) {
                    java.nio.file.Path dir = plugin.getDataFolder().toPath().resolve("pictures");
                    java.nio.file.Files.createDirectories(dir);
                    java.nio.file.Files.write(dir.resolve(id + "-step" + step + ".png"),
                            java.util.Base64.getDecoder().decode(png.substring(png.indexOf(',') + 1)));
                }
                return Optional.of(png);
            }

            @Override
            public void finished(String summary) {
                phase = "Done";
            }

            @Override
            public void error(String message) {
                sync(() -> plugin.announce(world, Component.text((side == null ? "" : who() + ": ") + "OpenRouter: " + message, NamedTextColor.RED), true));
            }
        };
    }

    private NamedTextColor colour() {
        return "B".equals(side) ? NamedTextColor.AQUA : NamedTextColor.GREEN;
    }

    private void sync(Runnable r) {
        if (plugin.isEnabled()) {
            Bukkit.getScheduler().runTask(plugin, r);
        }
    }

    private void refreshBar() {
        long elapsed = System.currentTimeMillis() - startedAt;
        String p = phase.equals("Thinking") ? "Thinking…" : phase;
        bar.name(Component.text(who(), colour())
                .append(Component.text("  ·  " + (step == 0 ? "" : "Step " + step + " of " + settings.buildCalls() + "  ·  ") + p
                        + "  ·  " + Text.count(placedTotal) + " blocks  ·  " + Text.clock(elapsed) + "  ·  " + Text.money(spent), NamedTextColor.WHITE)));
        bar.progress((float) Math.max(0, Math.min(1, elapsed / (double) (deadline - startedAt))));
        for (Player pl : Bukkit.getOnlinePlayers()) {
            if (pl.getWorld().equals(world)) {
                pl.showBossBar(bar);
            } else {
                pl.hideBossBar(bar);
            }
        }
    }

    private void cleanUp() {
        if (barTask != null) {
            barTask.cancel();
        }
        for (Player pl : Bukkit.getOnlinePlayers()) {
            pl.hideBossBar(bar);
        }
        for (Chunk c : tickets) {
            c.removePluginChunkTicket(plugin);
        }
        tickets.clear();
    }

    /** Main thread. */
    private void end(Agent.Result r) {
        cleanUp();
        long took = System.currentTimeMillis() - startedAt;
        String reason = r == null ? "failed" : r.reason();
        double cost = r == null ? spent : r.totals().cost;
        int builds = r == null ? step : r.builds();
        String name = who();
        String head = switch (reason) {
            case "called finish" -> name + " called it done";
            case "stopped" -> "Stopped by " + (stoppedBy == null ? "an operator" : stoppedBy);
            case "spend cap reached" -> name + " stopped: its next step could pass this build's " + Text.money(capUSD) + " cap";
            case "daily cap reached" -> name + " stopped: its next step could pass today's " + Text.money(settings.perDayUSD()) + " cap";
            case "time budget used up" -> name + " ran out of its " + settings.minutes() + " minutes";
            case "block budget used up" -> name + " used all " + Text.count(settings.blocks()) + " blocks";
            default -> name + " stopped (" + reason + ")";
        };
        plugin.announce(world, Component.text(head + ": ", colour())
                .append(Component.text(builds + (builds == 1 ? " step, " : " steps, ") + Text.count(placedTotal) + " blocks, "
                        + Text.clock(took) + ", " + Text.money(cost) + ".", NamedTextColor.GRAY)), false);
        if (r != null && r.summary() != null && !r.summary().isEmpty()) {
            plugin.announce(world, Component.text("\"" + r.summary() + "\"", NamedTextColor.GRAY), false);
        }
        result = builds + (builds == 1 ? " step, " : " steps, ") + Text.count(placedTotal) + " blocks, " + Text.clock(took) + ", " + Text.money(cost);
        plugin.getLogger().info("AI build " + id + " (" + model.id() + ", \"" + prompt + "\") ended: " + reason + ", " + builds + " build calls, "
                + placedTotal + " blocks, " + Text.clock(took) + ", $" + String.format(java.util.Locale.ROOT, "%.4f", cost)
                + (r == null ? "" : ", " + r.totals().totalTokens + " tokens"));
        plugin.ended(this);
    }
}
