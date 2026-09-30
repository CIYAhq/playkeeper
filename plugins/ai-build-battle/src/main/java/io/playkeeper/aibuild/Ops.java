package io.playkeeper.aibuild;

import com.google.gson.JsonArray;
import com.google.gson.JsonElement;
import com.google.gson.JsonObject;

import java.util.ArrayList;
import java.util.Comparator;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

/**
 * One build call's operations as the cells to change, in the order they're
 * placed: removals from the top down, then blocks from the ground up,
 * sweeping around the plot's centre, as the videos' builder does. A later
 * operation on a cell replaces an earlier one.
 */
final class Ops {
    /** More cells than any plot holds: a call asking for more is refused. */
    static final int MAX_CELLS = 400_000;

    /** A cell to change; {@code block} is null for a removal. */
    record Cell(int x, int y, int z, String block) {
    }

    record Plan(List<Cell> removals, List<Cell> placements, List<String> invalid, int skippedOutsidePlot) {
        int requested() {
            return removals.size() + placements.size();
        }
    }

    private Ops() {
    }

    static Plan plan(JsonArray ops, Frame frame) {
        Map<Long, Cell> desired = new LinkedHashMap<>();
        List<String> invalid = new ArrayList<>();
        int[] outside = {0};
        int i = 0;
        for (JsonElement e : ops == null ? new JsonArray() : ops) {
            i++;
            try {
                JsonObject op = e.getAsJsonObject();
                String kind = op.has("op") ? op.get("op").getAsString() : "";
                String block = op.has("block") && !op.get("block").isJsonNull() ? op.get("block").getAsString().trim() : null;
                switch (kind) {
                    case "remove" -> {
                        int[] a = xyz(op, op.has("from") ? "from" : "at");
                        int[] b = op.has("to") ? xyz(op, "to") : a;
                        if (!box(a, b, false, null, frame, desired, outside)) {
                            invalid.add("op " + i + ": too many blocks in one call");
                        }
                    }
                    case "place" -> {
                        if (block == null || block.isEmpty()) {
                            invalid.add("op " + i + ": no block");
                            continue;
                        }
                        int[] a = xyz(op, "at");
                        box(a, a, false, block, frame, desired, outside);
                    }
                    case "fill" -> {
                        if (block == null || block.isEmpty()) {
                            invalid.add("op " + i + ": no block");
                            continue;
                        }
                        boolean hollow = op.has("hollow") && op.get("hollow").getAsBoolean();
                        if (!box(xyz(op, "from"), xyz(op, "to"), hollow, block, frame, desired, outside)) {
                            invalid.add("op " + i + ": too many blocks in one call");
                        }
                    }
                    default -> invalid.add("op " + i + ": unknown op \"" + clip(kind) + "\"");
                }
            } catch (RuntimeException ex) {
                invalid.add("op " + i + ": " + clip(ex.getMessage() == null ? "can't read it" : ex.getMessage()));
            }
        }
        List<Cell> removals = new ArrayList<>();
        List<Cell> placements = new ArrayList<>();
        for (Cell c : desired.values()) {
            (c.block() == null ? removals : placements).add(c);
        }
        removals.sort(Comparator.comparingInt(Cell::y).reversed());
        placements.sort(Comparator.comparingInt(Cell::y).thenComparingDouble(c -> Math.atan2(c.z(), c.x())));
        return new Plan(removals, placements, invalid, outside[0]);
    }

    private static boolean box(int[] a, int[] b, boolean hollow, String block, Frame frame, Map<Long, Cell> desired, int[] outside) {
        int x0 = Math.min(a[0], b[0]), x1 = Math.max(a[0], b[0]);
        int y0 = Math.min(a[1], b[1]), y1 = Math.max(a[1], b[1]);
        int z0 = Math.min(a[2], b[2]), z1 = Math.max(a[2], b[2]);
        long volume = (long) (x1 - x0 + 1) * (y1 - y0 + 1) * (z1 - z0 + 1);
        if (volume > MAX_CELLS || desired.size() + volume > MAX_CELLS * 2L) {
            return false;
        }
        for (int y = y0; y <= y1; y++) {
            for (int x = x0; x <= x1; x++) {
                for (int z = z0; z <= z1; z++) {
                    if (hollow && x > x0 && x < x1 && y > y0 && y < y1 && z > z0 && z < z1) {
                        continue;
                    }
                    if (!frame.inPlot(x, y, z)) {
                        outside[0]++;
                        continue;
                    }
                    long k = key(x, y, z);
                    desired.remove(k);
                    desired.put(k, new Cell(x, y, z, block));
                }
            }
        }
        return true;
    }

    private static int[] xyz(JsonObject op, String field) {
        JsonArray a = op.getAsJsonArray(field);
        if (a == null || a.size() != 3) {
            throw new IllegalArgumentException("\"" + field + "\" needs [x, y, z]");
        }
        return new int[]{(int) Math.round(a.get(0).getAsDouble()), (int) Math.round(a.get(1).getAsDouble()), (int) Math.round(a.get(2).getAsDouble())};
    }

    private static long key(int x, int y, int z) {
        return ((long) (x + 1024) << 42) | ((long) (y + 1024) << 21) | (z + 1024);
    }

    static String clip(String s) {
        String t = s == null ? "" : s.replaceAll("\\s+", " ");
        return t.length() > 80 ? t.substring(0, 80) + "…" : t;
    }
}
