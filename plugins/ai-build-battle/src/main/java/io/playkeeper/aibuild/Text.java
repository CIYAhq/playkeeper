package io.playkeeper.aibuild;

import java.util.Locale;

final class Text {
    private Text() {
    }

    static String money(double usd) {
        if (usd > 0 && usd < 0.005) {
            return "<$0.01";
        }
        return String.format(Locale.ROOT, "$%.2f", usd);
    }

    static String count(int n) {
        return String.format(Locale.ROOT, "%,d", n);
    }

    static String clock(long millis) {
        long s = Math.max(0, millis / 1000);
        return s >= 3600 ? String.format(Locale.ROOT, "%d:%02d:%02d", s / 3600, s / 60 % 60, s % 60) : String.format(Locale.ROOT, "%d:%02d", s / 60, s % 60);
    }
}
