package io.playkeeper.aibuild;

import net.kyori.adventure.text.Component;
import net.kyori.adventure.text.event.ClickEvent;
import net.kyori.adventure.text.event.HoverEvent;
import net.kyori.adventure.text.format.NamedTextColor;
import net.kyori.adventure.text.format.TextDecoration;
import org.bukkit.Bukkit;
import org.bukkit.HeightMap;
import org.bukkit.Location;
import org.bukkit.World;
import org.bukkit.command.Command;
import org.bukkit.command.CommandSender;
import org.bukkit.command.TabExecutor;
import org.bukkit.entity.Player;
import org.bukkit.plugin.java.JavaPlugin;

import java.io.File;
import java.time.Clock;
import java.util.ArrayList;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.Optional;
import java.util.UUID;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.CopyOnWriteArrayList;

/**
 * AI Build Battle: {@code /aibuild <prompt>} has an AI model build the
 * prompt in front of the player, and {@code /aibattle <model A> <model B>
 * <prompt>} has two build it side by side, with the build battle videos'
 * tools, rules and limits, paid with the server owner's own OpenRouter key.
 */
public final class AIBuildBattlePlugin extends JavaPlugin implements TabExecutor {
    private static final long PENDING_FOR_MS = 120_000;
    private static final List<String> SUBCOMMANDS = List.of("stop", "status", "model", "reload", "help");

    private final OpenRouter api = new OpenRouter();
    private final Map<UUID, Pending> pending = new ConcurrentHashMap<>();
    /** What's building: one session, or a battle's two. */
    private final List<Session> sessions = new CopyOnWriteArrayList<>();
    private volatile boolean battle;
    private Settings settings;
    private Ledger ledger;

    /** One model's part of what's waiting for [Start]. */
    private record Part(OpenRouter.Model model, Frame frame, String side) {
    }

    private record Pending(String token, String prompt, World world, List<Part> parts, double capUSD, long at) {
    }

    @Override
    public void onEnable() {
        saveDefaultConfig();
        settings = Settings.from(getConfig());
        ledger = new Ledger(new File(getDataFolder(), "spend.yml"), Clock.systemUTC(), getLogger());
        for (String name : List.of("aibuild", "aibattle")) {
            var cmd = getCommand(name);
            if (cmd != null) {
                cmd.setExecutor(this);
                cmd.setTabCompleter(this);
            }
        }
        getLogger().info("Ready. Model " + settings.model() + ", up to " + Text.money(settings.perBuildUSD()) + " a build and "
                + Text.money(settings.perDayUSD()) + " a day. OpenRouter key " + (Keys.openRouter().isPresent() ? "set." : "not set yet."));
    }

    @Override
    public void onDisable() {
        for (Session s : sessions) {
            s.shutdown();
        }
        if (ledger != null) {
            ledger.settleAllAtWorst();
        }
    }

    OpenRouter api() {
        return api;
    }

    Ledger ledger() {
        return ledger;
    }

    /** A session ended; a battle's result comes once both have. */
    void ended(Session s) {
        List<Session> all = List.copyOf(sessions);
        boolean last = all.stream().allMatch(x -> x == s || x.result() != null);
        if (!last) {
            return;
        }
        sessions.clear();
        if (battle && all.size() == 2) {
            World w = Bukkit.getWorlds().isEmpty() ? null : s.world();
            if (w != null) {
                announce(w, Component.text("Both done. ", NamedTextColor.GREEN)
                        .append(Component.text(all.get(0).who() + ": " + all.get(0).result() + ". " + all.get(1).who() + ": " + all.get(1).result() + ".", NamedTextColor.GRAY)), false);
                announce(w, Component.text("Which one wins? A or B", NamedTextColor.WHITE, TextDecoration.BOLD), false);
            }
        }
        battle = false;
    }

    /** A line for everyone in the world, or only for those who run builds. */
    void announce(World world, Component line, boolean staffOnly) {
        Component msg = prefix().append(line);
        for (Player p : world.getPlayers()) {
            if (!staffOnly || p.hasPermission("aibuild.use")) {
                p.sendMessage(msg);
            }
        }
    }

    private static Component prefix() {
        return Component.text("[AI build] ", NamedTextColor.DARK_GREEN);
    }

    private static void tell(CommandSender to, Component line) {
        to.sendMessage(prefix().append(line));
    }

    private static void tell(CommandSender to, String line) {
        tell(to, Component.text(line, NamedTextColor.GRAY));
    }

    @Override
    public boolean onCommand(CommandSender sender, Command command, String label, String[] args) {
        if (command.getName().equalsIgnoreCase("aibattle")) {
            if (args.length == 1 && args[0].equalsIgnoreCase("stop")) {
                stop(sender);
            } else if (args.length < 3) {
                tell(sender, Component.text("/aibattle <model A> <model B> <what to build>", NamedTextColor.WHITE)
                        .append(Component.text(": two models build it side by side. Models are OpenRouter ids or " + String.join(", ", settings.aliases().keySet()) + ".", NamedTextColor.GRAY)));
            } else {
                String prompt = String.join(" ", java.util.Arrays.copyOfRange(args, 2, args.length)).trim();
                request(sender, List.of(settings.modelFor(args[0]), settings.modelFor(args[1])), prompt);
            }
            return true;
        }
        if (args.length == 0 || args.length == 1 && args[0].equalsIgnoreCase("help")) {
            help(sender);
            return true;
        }
        String first = args[0].toLowerCase(Locale.ROOT);
        if (args.length == 1) {
            switch (first) {
                case "stop" -> {
                    stop(sender);
                    return true;
                }
                case "status" -> {
                    status(sender);
                    return true;
                }
                case "reload" -> {
                    reloadConfig();
                    settings = Settings.from(getConfig());
                    tell(sender, "Settings read again. Model " + settings.model() + ", up to " + Text.money(settings.perBuildUSD())
                            + " a build and " + Text.money(settings.perDayUSD()) + " a day.");
                    return true;
                }
                case "cancel" -> {
                    if (sender instanceof Player p && pending.remove(p.getUniqueId()) != null) {
                        tell(sender, "Cancelled. Nothing was spent.");
                    }
                    return true;
                }
                case "model" -> {
                    tell(sender, "Model: " + settings.model() + ". Change it with /aibuild model <OpenRouter model id>.");
                    return true;
                }
                default -> {
                }
            }
        }
        if (first.equals("confirm") && args.length == 2) {
            confirm(sender, args[1]);
            return true;
        }
        if (first.equals("model") && args.length == 2 && (args[1].contains("/") || settings.aliases().containsKey(args[1].toLowerCase(Locale.ROOT)))) {
            setModel(sender, settings.modelFor(args[1]));
            return true;
        }
        request(sender, List.of(settings.model()), String.join(" ", args).trim());
        return true;
    }

    private void help(CommandSender to) {
        tell(to, Component.text("/aibuild <what to build>", NamedTextColor.WHITE)
                .append(Component.text(": " + settings.model() + " builds it in front of you, block by block.", NamedTextColor.GRAY)));
        tell(to, Component.text("/aibattle <model A> <model B> <what to build>", NamedTextColor.WHITE)
                .append(Component.text(": two models build it side by side.", NamedTextColor.GRAY)));
        tell(to, "/aibuild stop · /aibuild status · /aibuild model <id> · /aibuild reload");
    }

    private void status(CommandSender to) {
        if (sessions.isEmpty()) {
            tell(to, "No build running.");
        }
        for (Session s : sessions) {
            tell(to, s.status());
        }
        tell(to, "Model " + settings.model() + ". Today: " + Text.money(ledger.spentToday()) + " of " + Text.money(settings.perDayUSD())
                + " spent. Up to " + Text.money(settings.perBuildUSD()) + " a build. OpenRouter key " + (Keys.openRouter().isPresent() ? "set." : "not set."));
    }

    private void stop(CommandSender sender) {
        if (sessions.isEmpty()) {
            tell(sender, "No build running.");
            return;
        }
        boolean any = false;
        for (Session s : sessions) {
            any |= s.stop(sender.getName());
        }
        tell(sender, any ? "Stopping. What's placed stays." : "Already stopping.");
    }

    private void setModel(CommandSender sender, String id) {
        Bukkit.getScheduler().runTaskAsynchronously(this, () -> {
            String problem = null;
            try {
                Optional<OpenRouter.Model> m = api.model(id);
                if (m.isEmpty()) {
                    problem = "OpenRouter has no model " + id + ".";
                } else if (!m.get().tools()) {
                    problem = m.get().name() + " can't call tools, so it can't build.";
                }
            } catch (Exception e) {
                problem = "Couldn't reach OpenRouter's model list: " + Keys.scrub(e.getMessage(), null);
            }
            String why = problem;
            Bukkit.getScheduler().runTask(this, () -> {
                if (why != null) {
                    tell(sender, Component.text(why, NamedTextColor.RED));
                    return;
                }
                getConfig().set("model", id);
                saveConfig();
                settings = Settings.from(getConfig());
                tell(sender, "Model set to " + id + ".");
            });
        });
    }

    /** /aibuild and /aibattle: checks everything, then shows the caps before anything is spent. */
    private void request(CommandSender sender, List<String> modelIds, String prompt) {
        if (!(sender instanceof Player player)) {
            tell(sender, "Builds go in front of a player: run the command in the game.");
            return;
        }
        if (!sessions.isEmpty()) {
            tell(player, "A build is running. /aibuild stop ends it.");
            return;
        }
        if (prompt.isEmpty()) {
            help(player);
            return;
        }
        if (prompt.length() > 300) {
            tell(player, "That's a long one: keep it under 300 characters.");
            return;
        }
        Optional<String> key = Keys.openRouter();
        if (key.isEmpty()) {
            tell(player, Component.text("Set your OpenRouter key first: in the Playkeeper dashboard, open this server's Plugins tab, then AI Build Battle.", NamedTextColor.YELLOW));
            return;
        }
        Location loc = player.getLocation();
        List<Frame> frames = new ArrayList<>();
        try {
            frames.addAll(plotsFor(loc, modelIds.size()));
        } catch (IllegalStateException e) {
            tell(player, e.getMessage());
            return;
        }
        int inTheWay = 0;
        for (Frame f : frames) {
            inTheWay += blocksInTheWay(loc.getWorld(), f);
        }
        if (inTheWay > 0) {
            tell(player, "There's something in the way (" + Text.count(inTheWay) + " blocks) where the build would go. Face open ground and try again.");
            return;
        }
        Settings s = settings;
        World world = loc.getWorld();
        tell(player, "Checking your key and " + String.join(" and ", modelIds) + "…");
        String apiKey = key.get();
        Bukkit.getScheduler().runTaskAsynchronously(this, () -> {
            String problem = null;
            List<OpenRouter.Model> models = new ArrayList<>();
            OpenRouter.KeyStatus ks = null;
            try {
                for (String id : modelIds) {
                    Optional<OpenRouter.Model> m = api.model(id);
                    if (m.isEmpty()) {
                        problem = "OpenRouter has no model " + id + ". Use an OpenRouter model id, or one of " + String.join(", ", s.aliases().keySet()) + ".";
                        break;
                    }
                    if (!m.get().tools()) {
                        problem = m.get().name() + " can't call tools, so it can't build. Pick another model.";
                        break;
                    }
                    models.add(m.get());
                }
                if (problem == null) {
                    ks = api.key(apiKey);
                    if (!ks.ok()) {
                        problem = ks.status() == 401 || ks.status() == 403
                                ? "OpenRouter doesn't accept this key. Set a new one in the Playkeeper dashboard."
                                : "Couldn't check the key with OpenRouter (" + (ks.status() == 0 ? ks.message() : "answer " + ks.status()) + ").";
                    }
                }
            } catch (Exception e) {
                problem = "Couldn't reach OpenRouter: " + Keys.scrub(e.getMessage(), apiKey);
            }
            String why = problem;
            OpenRouter.KeyStatus status = ks;
            Bukkit.getScheduler().runTask(this, () -> {
                if (why != null) {
                    tell(player, Component.text(why, NamedTextColor.RED));
                    return;
                }
                List<Part> parts = new ArrayList<>();
                for (int i = 0; i < models.size(); i++) {
                    parts.add(new Part(models.get(i), frames.get(i), models.size() == 1 ? null : i == 0 ? "A" : "B"));
                }
                offer(player, prompt, world, parts, status, s);
            });
        });
    }

    private void offer(Player player, String prompt, World world, List<Part> parts, OpenRouter.KeyStatus key, Settings s) {
        int n = parts.size();
        double today = ledger.spentToday();
        double left = s.perDayUSD() - today;
        double cap = Math.min(s.perBuildUSD(), left / n);
        for (Part p : parts) {
            double least = Budget.smallestRequestUSD(p.model(), Budget.inputTokensHigh(firstMessages(prompt, p.model(), s))) + Budget.margin(Math.max(cap, 0.01));
            if (cap <= 0 || cap < least) {
                if (left / n < s.perBuildUSD()) {
                    tell(player, "Today's " + Text.money(s.perDayUSD()) + " is used up (" + Text.money(today) + " spent). It starts again at midnight UTC.");
                } else {
                    tell(player, p.model().name() + " needs at least " + Text.money(least) + " for one step at its prices, and a build may spend "
                            + Text.money(cap) + ". Raise spend.per-build in plugins/AIBuildBattle/config.yml.");
                }
                return;
            }
        }
        String token = Long.toString(Math.abs(new java.security.SecureRandom().nextLong()), 36);
        pending.put(player.getUniqueId(), new Pending(token, prompt, world, parts, cap, System.currentTimeMillis()));
        if (n == 1) {
            tell(player, Component.text(parts.get(0).model().name(), NamedTextColor.GREEN)
                    .append(Component.text(" will build \"" + prompt + "\" in front of you.", NamedTextColor.GRAY)));
        } else {
            tell(player, Component.text("A · " + parts.get(0).model().name(), NamedTextColor.GREEN)
                    .append(Component.text(" on the left and ", NamedTextColor.GRAY))
                    .append(Component.text("B · " + parts.get(1).model().name(), NamedTextColor.AQUA))
                    .append(Component.text(" on the right will both build \"" + prompt + "\".", NamedTextColor.GRAY)));
        }
        String each = n == 1 ? "for this build" : "for each, " + Text.money(cap * n) + " in all";
        String capLine = cap < s.perBuildUSD()
                ? "Up to " + Text.money(cap) + " " + each + ": what's left of today's " + Text.money(s.perDayUSD()) + "."
                : "Up to " + Text.money(cap) + " " + each + ". Today: " + Text.money(today) + " of " + Text.money(s.perDayUSD()) + " spent.";
        if (key != null && key.limitRemaining() != null && key.limitRemaining() < cap * n) {
            capLine += " Your key has " + Text.money(key.limitRemaining()) + " left.";
        }
        tell(player, capLine);
        if (!s.confirm()) {
            confirm(player, token);
            return;
        }
        player.sendMessage(Component.text("   ")
                .append(Component.text("[Start]", NamedTextColor.GREEN, TextDecoration.BOLD)
                        .clickEvent(ClickEvent.runCommand("/aibuild confirm " + token))
                        .hoverEvent(HoverEvent.showText(Component.text(n == 1 ? "Start the build" : "Start the battle"))))
                .append(Component.text("   "))
                .append(Component.text("[Cancel]", NamedTextColor.GRAY)
                        .clickEvent(ClickEvent.runCommand("/aibuild cancel"))
                        .hoverEvent(HoverEvent.showText(Component.text("Nothing is spent")))));
    }

    private static com.google.gson.JsonArray firstMessages(String prompt, OpenRouter.Model model, Settings s) {
        com.google.gson.JsonArray a = new com.google.gson.JsonArray();
        for (String[] m : new String[][]{{"system", Prompts.system(Bukkit.getMinecraftVersion(), s, model.pictures())}, {"user", Prompts.user(prompt)}}) {
            com.google.gson.JsonObject o = new com.google.gson.JsonObject();
            o.addProperty("role", m[0]);
            o.addProperty("content", m[1]);
            a.add(o);
        }
        return a;
    }

    private void confirm(CommandSender sender, String token) {
        if (!(sender instanceof Player player)) {
            return;
        }
        Pending p = pending.get(player.getUniqueId());
        if (p == null || !p.token().equals(token)) {
            return;
        }
        pending.remove(player.getUniqueId());
        if (System.currentTimeMillis() - p.at() > PENDING_FOR_MS) {
            tell(player, "That was a while ago: run it again.");
            return;
        }
        if (!sessions.isEmpty()) {
            tell(player, "A build is running. /aibuild stop ends it.");
            return;
        }
        Optional<String> key = Keys.openRouter();
        if (key.isEmpty()) {
            tell(player, Component.text("The OpenRouter key is gone: set it again in the Playkeeper dashboard.", NamedTextColor.YELLOW));
            return;
        }
        battle = p.parts().size() > 1;
        if (battle) {
            announce(p.world(), Component.text(player.getName() + " started a battle: ", NamedTextColor.GRAY)
                    .append(Component.text("A · " + p.parts().get(0).model().name(), NamedTextColor.GREEN))
                    .append(Component.text(" vs ", NamedTextColor.GRAY))
                    .append(Component.text("B · " + p.parts().get(1).model().name(), NamedTextColor.AQUA))
                    .append(Component.text(", \"" + p.prompt() + "\".", NamedTextColor.GRAY)), false);
        }
        List<Session> started = new ArrayList<>();
        for (Part part : p.parts()) {
            started.add(new Session(this, p.world(), part.frame(), part.model(), p.prompt(), player.getName(), p.capUSD(), settings, part.side()));
        }
        sessions.addAll(started);
        for (Session s : started) {
            getLogger().info(player.getName() + " started an AI build with " + s.modelId() + " (cap " + Text.money(p.capUSD()) + "): \"" + p.prompt() + "\"");
            s.start(key.get());
        }
    }

    /** The plots in front of the player, their ground level with theirs: one ahead, or two side by side. */
    private List<Frame> plotsFor(Location loc, int n) {
        Settings s = settings;
        World w = loc.getWorld();
        int ground = w.getHighestBlockYAt(loc.getBlockX(), loc.getBlockZ(), HeightMap.MOTION_BLOCKING_NO_LEAVES);
        ground = Math.min(ground, loc.getBlockY() - 1);
        int oy = ground + 1;
        if (oy - 1 < w.getMinHeight()) {
            throw new IllegalStateException("Stand on the ground first.");
        }
        int height = Math.min(s.height(), w.getMaxHeight() - 1 - oy);
        if (height < 16) {
            throw new IllegalStateException("Too close to the top of the world to build here.");
        }
        if (n == 1) {
            return List.of(Frame.inFrontOf(loc.getX(), loc.getZ(), loc.getYaw(), s.half() + 12, oy, s.half(), height));
        }
        // Side by side with a gap between them, far enough to see both.
        int side = s.half() + 6;
        int dist = s.half() + 30;
        return List.of(Frame.ahead(loc.getX(), loc.getZ(), loc.getYaw(), dist, -side, oy, s.half(), height),
                Frame.ahead(loc.getX(), loc.getZ(), loc.getYaw(), dist, side, oy, s.half(), height));
    }

    private static int blocksInTheWay(World w, Frame f) {
        int n = 0;
        for (int x = -f.half(); x <= f.half(); x++) {
            for (int z = -f.half(); z <= f.half(); z++) {
                int wx = f.worldX(x, z), wz = f.worldZ(x, z);
                int top = Math.min(w.getHighestBlockYAt(wx, wz, HeightMap.MOTION_BLOCKING_NO_LEAVES), f.worldY(f.height()));
                for (int y = f.worldY(0); y <= top; y++) {
                    if (w.getBlockAt(wx, y, wz).getType().isSolid()) {
                        n++;
                    }
                }
            }
        }
        return n;
    }

    @Override
    public List<String> onTabComplete(CommandSender sender, Command command, String alias, String[] args) {
        List<String> out = new ArrayList<>();
        String word = args.length == 0 ? "" : args[args.length - 1].toLowerCase(Locale.ROOT);
        if (command.getName().equalsIgnoreCase("aibattle")) {
            if (args.length <= 2) {
                for (String a : settings.aliases().keySet()) {
                    if (a.startsWith(word)) {
                        out.add(a);
                    }
                }
            }
            return out;
        }
        if (args.length == 1) {
            for (String s : SUBCOMMANDS) {
                if (s.startsWith(word)) {
                    out.add(s);
                }
            }
        }
        return out;
    }
}
