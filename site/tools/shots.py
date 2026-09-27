#!/usr/bin/env python3
"""Makes the site's screenshots in site/static/shots.

From the live demo: test/e2e/ui/site-captures.mjs takes them at twice the
pixels, and this writes each as <name>@2x.webp and, at half the size,
<name>.webp.

The demo has no sample data yet for a few screens (modpacks, sharing a pack,
importing a world, the map, schedules and AI agents), so those come from the
approved design frames (docs/marketing/designs.md in the project's store,
exported at 1x from Paper), at 1x only. Retake them from the demo once it has
them, and drop them from DESIGN_CROPS.

Usage: python3 site/tools/shots.py <captures-dir> [<designs-dir>]
Needs Pillow."""
import io
import os
import sys

from PIL import Image, ImageCms

OUT = "site/static/shots"

# name: (design file, box as left, top, right, bottom in the file's pixels)
DESIGN_CROPS = {
    "hero-map-phone": ("landing-desktop.png", (778, 474, 991, 905)),
    "step-size": ("landing-desktop.png", (142, 1198, 481, 1395)),
    "size-picker": ("landing-desktop.png", (605, 4776, 1319, 5057)),
    "feat-map": ("landing-desktop.png", (472, 3231, 663, 3336)),
    "feat-automation": ("landing-desktop.png", (776, 3231, 967, 3336)),
    "feat-agents": ("landing-desktop.png", (1080, 3231, 1271, 3336)),
    "step-modpack": ("feature-mods-and-modpacks-desktop.png", (553, 1448, 889, 1642)),
    "step-share": ("feature-mods-and-modpacks-desktop.png", (960, 1448, 1297, 1642)),
    "detail-modpack": ("feature-mods-and-modpacks-desktop.png", (121, 2447, 759, 2801)),
    "detail-voice": ("feature-mods-and-modpacks-desktop.png", (681, 2900, 1319, 3255)),
    "detail-pack": ("feature-mods-and-modpacks-desktop.png", (121, 3354, 759, 3710)),
    "move-aternos": ("alternative-aternos-desktop.png", (681, 2022, 1319, 2454)),
    "move-aternos-phone": ("alternative-aternos-phone.png", (20, 2400, 370, 2700)),
    "move-inside": ("alternative-pterodactyl-desktop.png", (681, 3406, 1319, 3839)),
    "guide-modpacks": ("guide-modded-minecraft-server-desktop.png", (520, 2416, 1280, 2838)),
    "guide-modpack-details": ("guide-modded-minecraft-server-desktop.png", (889, 2865, 1280, 3316)),
    "guide-share": ("guide-modded-minecraft-server-desktop.png", (520, 3949, 892, 4295)),
    "guide-pack-page": ("guide-modded-minecraft-server-desktop.png", (908, 3949, 1280, 4295)),
}


def srgb(path):
    """The design PNGs carry a Display P3 profile; the site is sRGB."""
    im = Image.open(path)
    icc = im.info.get("icc_profile")
    rgb = im.convert("RGB")
    if icc:
        rgb = ImageCms.profileToProfile(rgb, ImageCms.ImageCmsProfile(io.BytesIO(icc)), ImageCms.createProfile("sRGB"), outputMode="RGB")
    return rgb


def save(im, name):
    im.save(os.path.join(OUT, name + ".webp"), "WEBP", quality=84, method=6)


def main():
    captures = sys.argv[1]
    designs = sys.argv[2] if len(sys.argv) > 2 else ""
    os.makedirs(OUT, exist_ok=True)
    for f in sorted(os.listdir(captures)):
        if not f.endswith(".png") or f.startswith("_"):
            continue
        name = f[:-4]
        im = Image.open(os.path.join(captures, f)).convert("RGB")
        save(im, name + "@2x")
        save(im.resize((im.width // 2, im.height // 2), Image.LANCZOS), name)
        print(name, "from the demo")
    if designs:
        for name, (file, box) in DESIGN_CROPS.items():
            if os.path.exists(os.path.join(captures, name + ".png")):
                continue
            stale = os.path.join(OUT, name + "@2x.webp")
            if os.path.exists(stale):
                os.remove(stale)
            save(srgb(os.path.join(designs, file)).crop(box), name)
            print(name, "from the design frames")


if __name__ == "__main__":
    main()
