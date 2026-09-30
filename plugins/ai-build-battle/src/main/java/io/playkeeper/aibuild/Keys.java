package io.playkeeper.aibuild;

import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.Optional;
import java.util.regex.Pattern;

/**
 * Where the OpenRouter key comes from. On Playkeeper the dashboard keeps it
 * outside the server's folder and mounts it read-only into the container;
 * elsewhere it's an environment variable. It is read at the start of every
 * build, never stored by the plugin, and never written to chat, the console
 * or the log.
 */
final class Keys {
    static final Path PLAYKEEPER_FILE = Path.of("/run/playkeeper/secrets/openrouter_api_key");
    static final String ENV = "OPENROUTER_API_KEY";

    private static final Pattern LOOKS_LIKE_KEY = Pattern.compile("sk-[A-Za-z0-9_-]{8,}");

    private Keys() {
    }

    static Optional<String> openRouter() {
        return read(PLAYKEEPER_FILE, System.getenv(ENV));
    }

    static Optional<String> read(Path file, String env) {
        try {
            if (Files.isRegularFile(file)) {
                String k = Files.readString(file, StandardCharsets.UTF_8).trim();
                if (!k.isEmpty()) {
                    return Optional.of(k);
                }
            }
        } catch (IOException | SecurityException ignored) {
            // Unreadable counts as not set; the message says where to set it.
        }
        if (env != null && !env.isBlank()) {
            return Optional.of(env.trim());
        }
        return Optional.empty();
    }

    /** Text from elsewhere (an API's error, an exception) with any key taken out, cut to a line. */
    static String scrub(String text, String key) {
        if (text == null) {
            return "";
        }
        String s = text;
        if (key != null && key.length() >= 8) {
            s = s.replace(key, "[key]");
        }
        s = LOOKS_LIKE_KEY.matcher(s).replaceAll("[key]");
        s = s.replaceAll("\\s+", " ").trim();
        return s.length() > 240 ? s.substring(0, 240) + "…" : s;
    }
}
