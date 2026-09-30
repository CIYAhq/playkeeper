package io.playkeeper.aibuild;

import com.google.gson.JsonArray;
import com.google.gson.JsonObject;
import org.bukkit.Bukkit;
import org.bukkit.Material;
import org.bukkit.Sound;
import org.bukkit.SoundCategory;
import org.bukkit.World;
import org.bukkit.block.Block;
import org.bukkit.block.data.Bisected;
import org.bukkit.block.data.BlockData;
import org.bukkit.block.data.type.Bed;
import org.bukkit.block.data.type.Door;
import org.bukkit.plugin.Plugin;
import org.bukkit.scheduler.BukkitTask;

import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.concurrent.CompletableFuture;
import java.util.function.BooleanSupplier;
import java.util.function.IntConsumer;

/**
 * Carries out one build call on the main thread, a few blocks a tick, so
 * players watch the build appear, and reports back what happened the way the
 * videos' builder does.
 */
final class Placer {
    /** Not available to models, as in the videos, plus TNT, which a powered neighbour would set off. */
    static final Set<String> NOT_ALLOWED = Set.of("water", "lava", "bubble_column", "fire", "soul_fire", "nether_portal", "end_portal",
            "end_gateway", "piston_head", "moving_piston", "air", "cave_air", "void_air", "barrier", "command_block", "chain_command_block",
            "repeating_command_block", "structure_block", "structure_void", "jigsaw", "light", "bedrock", "spawner", "trial_spawner", "vault",
            "reinforced_deepslate", "tnt", "test_block", "test_instance_block");
    private static final Set<Material> TALL_PLANTS = Set.of(Material.SUNFLOWER, Material.LILAC, Material.ROSE_BUSH, Material.PEONY,
            Material.TALL_GRASS, Material.LARGE_FERN, Material.PITCHER_PLANT);
    private static final int MAX_FAILURES = 12;

    private final Plugin plugin;
    private final World world;
    private final Frame frame;
    private final double perTick;
    private final long deadline;
    private final int blockBudget;
    private final int usedBefore;
    private final BooleanSupplier stopped;
    private final IntConsumer onPlaced;
    private final CompletableFuture<JsonObject> done = new CompletableFuture<>();
    private final Map<String, Object> parsed = new HashMap<>();
    private final ArrayDeque<Ops.Cell> removals;
    private ArrayDeque<Ops.Cell> todo;
    private final List<Ops.Cell> deferred = new ArrayList<>();
    private boolean deferredRound;
    private final Ops.Plan plan;
    private int placed, removed, unchanged, failed;
    private final List<String> failures = new ArrayList<>();
    private final List<String> invalid;
    private String stoppedEarly;
    private double credit;
    private BukkitTask task;

    Placer(Plugin plugin, World world, Frame frame, Ops.Plan plan, double blocksPerSecond, long deadline, int blockBudget, int usedBefore,
           BooleanSupplier stopped, IntConsumer onPlaced) {
        this.plugin = plugin;
        this.world = world;
        this.frame = frame;
        this.plan = plan;
        this.perTick = blocksPerSecond / 20.0;
        this.deadline = deadline;
        this.blockBudget = blockBudget;
        this.usedBefore = usedBefore;
        this.stopped = stopped;
        this.onPlaced = onPlaced;
        this.removals = new ArrayDeque<>(plan.removals());
        this.todo = new ArrayDeque<>(plan.placements());
        this.invalid = new ArrayList<>(plan.invalid());
    }

    /** Starts on the main thread; the report arrives when the last block is down or the build stops. */
    CompletableFuture<JsonObject> start() {
        Bukkit.getScheduler().runTask(plugin, () -> task = Bukkit.getScheduler().runTaskTimer(plugin, this::tick, 0, 1));
        return done;
    }

    void cancel() {
        if (task != null) {
            task.cancel();
        }
        done.complete(report());
    }

    private void tick() {
        if (done.isDone()) {
            task.cancel();
            return;
        }
        credit = Math.min(credit + perTick, Math.max(1, perTick * 2));
        // Cells that need no change don't use up the rate, but a tick does only so many.
        int free = 0;
        while (credit >= 1 && free < 20_000) {
            String stop = stopReason();
            if (stop != null) {
                stoppedEarly = stop;
                finish();
                return;
            }
            Ops.Cell c = removals.poll();
            boolean removal = c != null;
            if (c == null) {
                if (usedBefore + placed >= blockBudget && (!todo.isEmpty() || !deferred.isEmpty())) {
                    stoppedEarly = "block budget";
                    finish();
                    return;
                }
                c = nextPlacement();
            }
            if (c == null) {
                finish();
                return;
            }
            if (removal ? remove(c) : place(c)) {
                credit -= 1;
            } else {
                free++;
            }
        }
    }

    private Ops.Cell nextPlacement() {
        Ops.Cell c = todo.poll();
        if (c == null && !deferredRound && !deferred.isEmpty()) {
            deferredRound = true;
            todo = new ArrayDeque<>(deferred);
            deferred.clear();
            c = todo.poll();
        }
        return c;
    }

    private String stopReason() {
        if (stopped.getAsBoolean()) {
            return "stopped";
        }
        if (System.currentTimeMillis() > deadline) {
            return "time budget";
        }
        return null;
    }

    private Block at(Ops.Cell c) {
        return world.getBlockAt(frame.worldX(c.x(), c.z()), frame.worldY(c.y()), frame.worldZ(c.x(), c.z()));
    }

    /** True when it took a turn of the placing rate. */
    private boolean remove(Ops.Cell c) {
        Block b = at(c);
        if (b.getType().isAir()) {
            unchanged++;
            return false;
        }
        b.setType(Material.AIR, false);
        removed++;
        return true;
    }

    private boolean place(Ops.Cell c) {
        Object p = parsed.computeIfAbsent(c.block(), this::parse);
        if (p instanceof String why) {
            if (invalid.size() < MAX_FAILURES && !invalid.contains(why)) {
                invalid.add(why);
            }
            failed++;
            return false;
        }
        BlockData data = ((BlockData) p).clone();
        if (autoHalf(data)) {
            return false;
        }
        Block b = at(c);
        if (b.getBlockData().equals(data)) {
            unchanged++;
            return false;
        }
        if (!data.isSupported(b)) {
            if (!deferredRound) {
                deferred.add(c);
            } else {
                fail(c, "needs a solid block to stand on or hang from");
            }
            return false;
        }
        b.setBlockData(data, false);
        placeOtherHalf(b, data);
        placed++;
        onPlaced.accept(usedBefore + placed);
        if (placed % 3 == 0) {
            world.playSound(b.getLocation().add(0.5, 0.5, 0.5), placeSound(data), SoundCategory.BLOCKS, 0.6f, 1f);
        }
        return true;
    }

    private static Sound placeSound(BlockData data) {
        try {
            return data.getSoundGroup().getPlaceSound();
        } catch (RuntimeException e) {
            return Sound.BLOCK_STONE_PLACE;
        }
    }

    private void fail(Ops.Cell c, String why) {
        failed++;
        if (failures.size() < MAX_FAILURES) {
            failures.add(c.block() + " at [" + c.x() + ", " + c.y() + ", " + c.z() + "]: " + why);
        }
    }

    /** The block, turned to the world's frame, or why it can't be placed. */
    private Object parse(String spec) {
        String s = spec.startsWith("minecraft:") ? spec.substring("minecraft:".length()) : spec;
        int bracket = s.indexOf('[');
        String name = (bracket < 0 ? s : s.substring(0, bracket)).trim().toLowerCase(java.util.Locale.ROOT);
        if (NOT_ALLOWED.contains(name)) {
            return "\"" + Ops.clip(name) + "\" isn't allowed in this challenge";
        }
        BlockData data;
        try {
            data = Bukkit.createBlockData("minecraft:" + s.trim());
        } catch (IllegalArgumentException e) {
            return "can't read block \"" + Ops.clip(spec) + "\"";
        }
        Material m = data.getMaterial();
        if (!m.isBlock() || m.isAir()) {
            return "\"" + Ops.clip(name) + "\" isn't a block";
        }
        Material item = data.getPlacementMaterial();
        if (item == null || item.isAir() || !item.isItem()) {
            return "\"" + Ops.clip(name) + "\" has no item a player can place";
        }
        data.rotate(frame.rotation());
        return data;
    }

    /** The second half of doors, beds and tall plants, which the game adds itself. */
    private static boolean autoHalf(BlockData d) {
        if (d instanceof Door door) {
            return door.getHalf() == Bisected.Half.TOP;
        }
        if (d instanceof Bed bed) {
            return bed.getPart() == Bed.Part.HEAD;
        }
        return TALL_PLANTS.contains(d.getMaterial()) && d instanceof Bisected b && b.getHalf() == Bisected.Half.TOP;
    }

    private static void placeOtherHalf(Block b, BlockData d) {
        if (d instanceof Door || (TALL_PLANTS.contains(d.getMaterial()) && d instanceof Bisected)) {
            Block up = b.getRelative(0, 1, 0);
            if (up.getType().isAir() || up.isReplaceable()) {
                Bisected top = (Bisected) d.clone();
                top.setHalf(Bisected.Half.TOP);
                up.setBlockData(top, false);
            }
        } else if (d instanceof Bed bed) {
            Block head = b.getRelative(bed.getFacing());
            if (head.getType().isAir() || head.isReplaceable()) {
                Bed h = (Bed) bed.clone();
                h.setPart(Bed.Part.HEAD);
                head.setBlockData(h, false);
            }
        }
    }

    private void finish() {
        if (task != null) {
            task.cancel();
        }
        for (Ops.Cell c : deferred) {
            fail(c, "needs a solid block to stand on or hang from");
        }
        deferred.clear();
        done.complete(report());
    }

    private JsonObject report() {
        JsonObject r = new JsonObject();
        r.addProperty("requested", plan.requested());
        r.addProperty("placed", placed);
        r.addProperty("removed", removed);
        r.addProperty("unchanged", unchanged);
        r.addProperty("failed", failed);
        r.addProperty("skippedOutsidePlot", plan.skippedOutsidePlot());
        JsonArray inv = new JsonArray();
        invalid.stream().limit(MAX_FAILURES).forEach(inv::add);
        r.add("invalid", inv);
        JsonArray f = new JsonArray();
        failures.forEach(f::add);
        r.add("failures", f);
        if (stoppedEarly == null) {
            r.add("stoppedEarly", com.google.gson.JsonNull.INSTANCE);
        } else {
            r.addProperty("stoppedEarly", stoppedEarly);
        }
        r.addProperty("totalPlaced", usedBefore + placed);
        return r;
    }

    int placed() {
        return placed;
    }
}
