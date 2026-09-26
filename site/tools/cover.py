#!/usr/bin/env python3
"""Draws site/static/img/blog/playkeeper-0-4-0.svg, the 0.4.0 post's cover:
a night scene with the version on a sign, Pip cheering by a campfire.
Original pixel art in 26 px blocks; Pip is the dashboard's pip-cheer.svg."""
import re

B, COLS, ROWS = 26, 40, 17
SKY, SKY_TOP = "#27335b", "#34416d"
STAR = "#ffffff"
GRASS, GRASS_DARK = "#7ec06a", "#5f9e4c"
DIRT, DIRT_DARK = "#9b6f47", "#7d5635"
CROWN, CROWN_DARK, TRUNK = "#6aae5e", "#4d8b45", "#7d5635"
LOG_TOP, LOG = "#b88a5b", "#9b6f47"
FLAME, FLAME_HOT, FLAME_TIP = "#f2994a", "#ffc83d", "#fde29a"

cells = {}
def put(c, r, colour):
    if 0 <= c < COLS and 0 <= r < ROWS:
        cells[(c, r)] = colour

for c in range(COLS):
    put(c, 0, SKY_TOP)
for (c, r) in ((0, 1), (5, 0), (11, 2), (13, 3), (27, 0), (31, 1), (32, 0), (36, 2), (38, 5), (22, 4), (1, 6), (34, 7)):
    put(c, r, STAR)
# The tree on the left.
for r in range(9, 13):
    put(4, r, TRUNK)
for c in range(2, 7):
    for r in range(4, 9):
        if (c in (2, 6)) and r in (4, 8):
            continue
        put(c, r, CROWN)
for (c, r) in ((4, 5), (3, 6), (5, 7), (3, 8)):
    put(c, r, CROWN_DARK)
# Two log benches.
for c in range(9, 14):
    put(c, 11, LOG_TOP); put(c, 12, LOG)
for c in range(31, 36):
    put(c, 11, LOG_TOP); put(c, 12, LOG)
# The campfire.
for (c, r, colour) in ((23, 7, FLAME_TIP), (23, 8, FLAME_HOT), (24, 9, FLAME_HOT), (22, 9, FLAME), (23, 9, FLAME_HOT),
                       (22, 10, FLAME), (23, 10, FLAME), (24, 10, FLAME), (21, 11, FLAME), (22, 11, FLAME_HOT), (23, 11, FLAME),
                       (24, 11, FLAME_HOT), (25, 11, FLAME), (21, 12, TRUNK), (22, 12, LOG), (24, 12, LOG), (25, 12, TRUNK)):
    put(c, r, colour)
# The ground.
for c in range(COLS):
    put(c, 13, GRASS if (c // 3) % 2 == 0 else GRASS_DARK)
    for r in range(14, ROWS):
        put(c, r, DIRT_DARK if (c * 7 + r * 13) % 11 == 0 else DIRT)

rects = [f'<rect width="{COLS * B}" height="{ROWS * B}" fill="{SKY}"/>']
for r in range(ROWS):
    c = 0
    while c < COLS:
        colour = cells.get((c, r))
        if colour is None:
            c += 1
            continue
        start = c
        while c < COLS and cells.get((c, r)) == colour:
            c += 1
        rects.append(f'<rect x="{start * B}" y="{r * B}" width="{(c - start) * B}" height="{B}" fill="{colour}"/>')

pip = open("web/src/assets/pip/pip-cheer.svg").read()
inner = re.sub(r"^<svg[^>]*>|</svg>\s*$", "", pip.strip())
vb = re.search(r'viewBox="([^"]+)"', pip).group(1)
pip_svg = f'<svg x="{17 * B}" y="{7 * B - 8}" width="{6 * B}" height="{6 * B}" viewBox="{vb}" shape-rendering="geometricPrecision">{inner}</svg>'

sign = (f'<g transform="translate({2 * B - 6} {1 * B + 8})">'
        f'<rect x="4" y="6" width="{5 * B}" height="{2 * B + 12}" rx="14" fill="#1d211c"/>'
        f'<rect width="{5 * B}" height="{2 * B + 12}" rx="14" fill="#ffc83d" stroke="#1d211c" stroke-width="4"/>'
        f'<text x="{5 * B / 2}" y="{B + 23}" text-anchor="middle" font-family="system-ui, -apple-system, Segoe UI, Roboto, sans-serif" '
        f'font-size="44" font-weight="800" letter-spacing="-1" fill="#1d211c">0.4.0</text></g>')

svg = (f'<svg xmlns="http://www.w3.org/2000/svg" width="{COLS * B}" height="{ROWS * B}" viewBox="0 0 {COLS * B} {ROWS * B}" '
       f'shape-rendering="crispEdges">' + "".join(rects) + sign + pip_svg + "</svg>\n")
open("site/static/img/blog/playkeeper-0-4-0.svg", "w").write(svg)
print(len(svg), "bytes")
