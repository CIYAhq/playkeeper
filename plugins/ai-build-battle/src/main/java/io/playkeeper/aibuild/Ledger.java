package io.playkeeper.aibuild;

import org.bukkit.configuration.file.YamlConfiguration;

import java.io.File;
import java.io.IOException;
import java.time.Clock;
import java.time.LocalDate;
import java.time.ZoneOffset;
import java.util.HashMap;
import java.util.Map;
import java.util.logging.Logger;

/**
 * Today's spend, for the daily cap: what OpenRouter reported for each
 * request, plus the worst case of requests whose answers were lost. Requests
 * in flight reserve their worst case, so builds running together can't pass
 * the cap between them. The day starts at midnight UTC.
 */
final class Ledger {
    private final File file;
    private final Clock clock;
    private final Logger log;
    private final Map<String, Double> reserved = new HashMap<>();
    private LocalDate day;
    private double spent;

    Ledger(File file, Clock clock, Logger log) {
        this.file = file;
        this.clock = clock;
        this.log = log;
        YamlConfiguration y = YamlConfiguration.loadConfiguration(file);
        this.day = parse(y.getString("day"));
        this.spent = Math.max(0, y.getDouble("spent", 0));
        roll();
    }

    private static LocalDate parse(String s) {
        try {
            return s == null ? null : LocalDate.parse(s);
        } catch (RuntimeException e) {
            return null;
        }
    }

    private void roll() {
        LocalDate today = LocalDate.now(clock.withZone(ZoneOffset.UTC));
        if (!today.equals(day)) {
            day = today;
            spent = 0;
        }
    }

    synchronized double spentToday() {
        roll();
        return spent;
    }

    /** Spent today plus what other requests in flight may still cost. */
    synchronized double committedExcept(String key) {
        roll();
        double r = 0;
        for (Map.Entry<String, Double> e : reserved.entrySet()) {
            if (!e.getKey().equals(key)) {
                r += e.getValue();
            }
        }
        return spent + r;
    }

    synchronized void reserve(String key, double worstUSD) {
        reserved.put(key, Math.max(0, worstUSD));
    }

    /** A request's answer came (or was lost): count what it cost and free its reservation. */
    synchronized void settle(String key, double chargedUSD) {
        roll();
        reserved.remove(key);
        spent += Math.max(0, chargedUSD);
        save();
    }

    /** Requests still in flight when the server stops are counted at their worst case. */
    synchronized void settleAllAtWorst() {
        roll();
        for (double v : reserved.values()) {
            spent += v;
        }
        reserved.clear();
        save();
    }

    private void save() {
        YamlConfiguration y = new YamlConfiguration();
        y.options().setHeader(java.util.List.of("Today's AI build spend in US dollars, from OpenRouter's reported costs. The day starts at midnight UTC."));
        y.set("day", day.toString());
        y.set("spent", Math.round(spent * 1e6) / 1e6);
        try {
            file.getParentFile().mkdirs();
            y.save(file);
        } catch (IOException e) {
            log.warning("Couldn't save today's spend to " + file.getName() + ": " + e.getMessage());
        }
    }
}
