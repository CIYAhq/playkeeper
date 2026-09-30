package io.playkeeper.aibuild;

import org.bukkit.configuration.file.FileConfiguration;

/** config.yml, read once at start and on /aibuild reload. */
record Settings(
        String model,
        double perBuildUSD,
        double perDayUSD,
        int buildCalls,
        int blocks,
        int minutes,
        int half,
        int height,
        double blocksPerSecond,
        boolean confirm,
        String reasoningEffort,
        int maxTokens,
        boolean savePictures,
        java.util.Map<String, String> aliases) {

    /** The fewest output tokens a request may have; below it the model stops, as in the videos. */
    static final int MIN_OUTPUT_TOKENS = 8000;

    static Settings from(FileConfiguration c) {
        return new Settings(
                c.getString("model", "anthropic/claude-sonnet-5.5").trim(),
                clamp(c.getDouble("spend.per-build", 1.00), 0.01, 100),
                clamp(c.getDouble("spend.per-day", 5.00), 0.01, 1000),
                (int) clamp(c.getInt("limits.build-calls", 10), 1, 30),
                (int) clamp(c.getInt("limits.blocks", 6000), 100, 50000),
                (int) clamp(c.getInt("limits.minutes", 45), 2, 180),
                (int) clamp(c.getInt("plot.half-width", 30), 8, 64),
                (int) clamp(c.getInt("plot.height", 80), 16, 200),
                clamp(c.getDouble("blocks-per-second", 20), 1, 400),
                c.getBoolean("confirm", true),
                c.getString("request.reasoning-effort", "high").trim(),
                (int) clamp(c.getInt("request.max-tokens", 48000), MIN_OUTPUT_TOKENS, 128000),
                c.getBoolean("save-pictures", false),
                aliases(c));
    }

    private static java.util.Map<String, String> aliases(FileConfiguration c) {
        java.util.Map<String, String> out = new java.util.TreeMap<>();
        var sec = c.getConfigurationSection("aliases");
        if (sec != null) {
            for (String k : sec.getKeys(false)) {
                String v = sec.getString(k, "").trim();
                if (!v.isEmpty()) {
                    out.put(k.toLowerCase(java.util.Locale.ROOT), v);
                }
            }
        }
        return java.util.Collections.unmodifiableMap(out);
    }

    /** A model id, or the id an alias stands for. */
    String modelFor(String name) {
        return aliases.getOrDefault(name.toLowerCase(java.util.Locale.ROOT), name);
    }

    private static double clamp(double v, double lo, double hi) {
        return Double.isNaN(v) ? lo : Math.max(lo, Math.min(hi, v));
    }
}
