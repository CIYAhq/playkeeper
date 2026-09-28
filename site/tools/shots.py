#!/usr/bin/env python3
"""Makes the site's screenshots in site/static/shots.

test/e2e/ui/site-captures.mjs takes them from the dashboard, at 2 to 4 times
their pixels. This writes each at the widths the pages' srcset offers, as AVIF
and, for browsers without AVIF, WebP: <name>-<width>w.avif and .webp.

SLOTS says how wide the site shows each screenshot, in CSS pixels: on a
desktop, at most in any layout (the pages' sizes attributes say the same), and
on a phone 390 pixels wide. The widths are those at 1 and 2 times the desktop
size, 3 times the phone size and 2 times the largest, less any within 15% of
a larger one; screenshots in a phone frame get 1, 2 and 3 times the largest.
A capture narrower than the widest of them is an error: nothing is ever
enlarged.

Usage: python3 site/tools/shots.py <captures-dir>
Needs Pillow 11.3 or newer, for AVIF."""
import os
import re
import sys

from PIL import Image

OUT = "site/static/shots"

# In a phone frame, on every screen.
PHONES = {"hero-map-phone", "feat-types", "feat-address", "feat-backups", "feat-crash", "feat-friends", "feat-map", "feat-automation", "feat-agents"}

# name: (desktop, largest, phone); None where the page hides it.
SLOTS = {
    # The landing page: its product loop, phones, steps and live demo window.
    "loop-overview": (858, 858, None),
    "loop-new-server": (858, 858, None),
    "loop-setting-up": (858, 858, None),
    "loop-setting-up-2": (858, 858, None),
    "hero-map-phone": (198, 272, 272),
    "step-size": (363, 574, 340),
    "feat-types": (182, 182, 126),
    "feat-address": (182, 182, 126),
    "feat-backups": (182, 182, 126),
    "feat-crash": (182, 182, 126),
    "feat-friends": (182, 182, 126),
    "feat-map": (182, 182, 126),
    "feat-automation": (182, 182, 126),
    "feat-agents": (182, 182, 126),
    "demo-home": (706, 958, 348),
    # Mods and modpacks.
    "feature-mods": (998, 998, 348),
    "step-type": (363, 574, 340),
    "step-modpack": (363, 574, 340),
    "step-share": (363, 574, 340),
    "detail-browse": (564, 960, 350),
    "detail-modpack": (564, 960, 350),
    "detail-voice": (564, 960, 350),
    "detail-pack": (564, 960, 350),
    "detail-updates": (564, 960, 350),
    # The alternatives.
    "move-aternos": (708, 960, None),
    "move-aternos-phone": (None, 600, 350),
    "ptero-hero": (558, 558, None),
    "ptero-world": (672, 958, 348),
    "move-inside": (708, 960, 350),
    # The modded server guide and the 0.4.0 post.
    "guide-modpacks": (760, 960, 350),
    "guide-modpack-details": (402, 600, 350),
    "guide-mods-tab": (760, 960, 350),
    "guide-share": (372, 600, 350),
    "guide-pack-page": (372, 600, 350),
    "guide-crash": (760, 960, 350),
    # The play-with-friends and add-mods guides.
    "guide-address": (760, 960, 350),
    "guide-invite": (760, 960, 350),
    "guide-files": (760, 960, 350),
    "post-mods": (730, 730, 350),
    "post-address": (730, 730, 350),
    "post-backups": (730, 730, 350),
    # The free tools.
    "tool-server-list": (460, 560, 350),
    "tool-plugin-config": (460, 560, 350),
    "tool-memory": (460, 560, 350),
    "tool-properties": (460, 560, 350),
}


def widths(name):
    desktop, largest, phone = SLOTS[name]
    if name in PHONES:
        return [largest, 2 * largest, 3 * largest]
    want = {2 * largest}
    if desktop:
        want |= {desktop, 2 * desktop}
    if phone:
        want |= {3 * phone} if desktop else {2 * phone, 3 * phone}
    out = []
    for w in sorted(want, reverse=True):
        if not out or w * 1.15 <= out[-1]:
            out.append(w)
    return sorted(out)


def main():
    captures = sys.argv[1]
    os.makedirs(OUT, exist_ok=True)
    made = []
    for f in sorted(os.listdir(captures)):
        if not f.endswith(".png") or f.startswith("_"):
            continue
        name = f[:-4]
        if name not in SLOTS:
            sys.exit(f"{f}: add how wide the site shows it to SLOTS")
        im = Image.open(os.path.join(captures, f)).convert("RGB")
        want = widths(name)
        if im.width < want[-1]:
            sys.exit(f"{f} is {im.width} pixels wide; the site needs {want[-1]}. Take it at a higher pixel ratio.")
        for old in os.listdir(OUT):
            if re.fullmatch(re.escape(name) + r"(@2x|-\d+w)?\.(webp|avif)", old):
                os.remove(os.path.join(OUT, old))
        for w in want:
            small = im if w == im.width else im.resize((w, round(im.height * w / im.width)), Image.LANCZOS)
            base = os.path.join(OUT, f"{name}-{w}w")
            small.save(base + ".avif", "AVIF", quality=80, subsampling="4:4:4", speed=4)
            small.save(base + ".webp", "WEBP", quality=90, method=6)
        made.append(name)
        print(name, " ".join(f"{w}w" for w in want))
    missing = sorted(set(SLOTS) - set(made))
    if missing:
        print("not in the captures, left as they were:", ", ".join(missing))


if __name__ == "__main__":
    main()
