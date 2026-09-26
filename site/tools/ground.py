#!/usr/bin/env python3
"""Draws site/static/img/ground.svg: the pixel ground under the landing hero
and the closing band. Original pixel art, 6 px blocks, 80 x 12 blocks, and
seamless left to right so it repeats. Run it after changing the shapes."""
import random

COLS, ROWS, B = 80, 12, 6
GRASS, GRASS_DARK = "#86c46b", "#5f9e4c"
DIRT, DIRT_DARK = "#9b6f47", "#7d5635"
CROWN, CROWN_DARK, TRUNK = "#6aae5e", "#4d8b45", "#7d5635"
FLOWER = "#ffc83d"

# Surface height in blocks, per column: gentle hills that meet at the edges.
heights = []
for c in range(COLS):
    wave = [4, 4, 4, 5, 5, 5, 6, 6, 6, 6, 5, 5, 4, 4, 4, 4, 3, 3, 3, 4]
    heights.append(wave[(c * len(wave)) // COLS % len(wave)])
# Two taller hills.
for c in range(24, 34):
    heights[c] += 1
for c in range(58, 67):
    heights[c] += 1

rng = random.Random(7)
cells = {}  # (col, row from top) -> colour
for c, h in enumerate(heights):
    top = ROWS - h
    cells[(c, top)] = GRASS
    # The step down to a lower neighbour shows darker grass on its side.
    left, right = heights[c - 1], heights[(c + 1) % COLS]
    if left < h or right < h:
        cells[(c, top)] = GRASS_DARK if rng.random() < 0.5 else GRASS
    for r in range(top + 1, ROWS):
        cells[(c, r)] = DIRT_DARK if rng.random() < 0.16 else DIRT
    if r := (top + 1 if rng.random() < 0.35 else None):
        cells[(c, r)] = GRASS_DARK

def tree(c):
    top = ROWS - heights[c]
    trunk = [top - 1, top - 2]
    for r in trunk:
        cells[(c, r)] = TRUNK
    crown_top = top - 2 - 5
    for dc in range(-2, 3):
        for dr in range(5):
            if abs(dc) == 2 and dr in (0, 4):
                continue
            cells[((c + dc) % COLS, crown_top + dr)] = CROWN
    for dc, dr in ((-1, 1), (1, 2), (0, 3), (-1, 3)):
        cells[((c + dc) % COLS, crown_top + dr)] = CROWN_DARK

for c in (9, 29, 49, 70):
    tree(c)
for c in (3, 16, 26, 33, 44, 62, 66, 77):
    cells[(c, ROWS - heights[c] - 1)] = FLOWER

# Merge horizontal runs of one colour into one rect each.
rects = []
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

svg = (f'<svg xmlns="http://www.w3.org/2000/svg" width="{COLS * B}" height="{ROWS * B}" viewBox="0 0 {COLS * B} {ROWS * B}" '
       f'shape-rendering="crispEdges">' + "".join(rects) + "</svg>\n")
open("site/static/img/ground.svg", "w").write(svg)
print(len(rects), "rects,", len(svg), "bytes")


def hills():
    """The landing hero's ground: taller hills, with pale hills behind."""
    cols, rows = 120, 18
    back, front = {}, {}
    far = "#dcebd3"
    for c in range(cols):
        hb = 9 + round(3 * __import__("math").sin(c / 9.0) + 2 * __import__("math").sin(c / 4.1 + 1))
        for r in range(rows - hb, rows):
            back[(c, r)] = far
    heights = []
    for c in range(cols):
        m = __import__("math")
        h = 6 + round(3.2 * m.sin(2 * m.pi * c / cols * 2) + 1.6 * m.sin(2 * m.pi * c / cols * 5 + 0.7))
        heights.append(max(3, h))
    rng2 = random.Random(11)
    for c, h in enumerate(heights):
        top = rows - h
        left, right = heights[c - 1], heights[(c + 1) % cols]
        front[(c, top)] = GRASS_DARK if (left < h or right < h) and rng2.random() < 0.6 else GRASS
        for r in range(top + 1, rows):
            front[(c, r)] = DIRT_DARK if rng2.random() < 0.17 else DIRT
    def tree2(c):
        top = rows - heights[c]
        for r in (top - 1, top - 2):
            front[(c, r)] = TRUNK
        ct = top - 7
        for dc in range(-2, 3):
            for dr in range(5):
                if abs(dc) == 2 and dr in (0, 4):
                    continue
                front[((c + dc) % cols, ct + dr)] = CROWN
        for dc, dr in ((-1, 1), (1, 2), (0, 3)):
            front[((c + dc) % cols, ct + dr)] = CROWN_DARK
    for c in (7, 33, 58, 86, 108):
        tree2(c)
    for c in (15, 24, 46, 71, 79, 97, 116):
        front[(c, rows - heights[c] - 1)] = FLOWER
    out = []
    for layer in (back, front):
        for r in range(rows):
            c = 0
            while c < cols:
                colour = layer.get((c, r))
                if colour is None:
                    c += 1
                    continue
                start = c
                while c < cols and layer.get((c, r)) == colour:
                    c += 1
                out.append(f'<rect x="{start * B}" y="{r * B}" width="{(c - start) * B}" height="{B}" fill="{colour}"/>')
    svg = (f'<svg xmlns="http://www.w3.org/2000/svg" width="{cols * B}" height="{rows * B}" viewBox="0 0 {cols * B} {rows * B}" '
           f'shape-rendering="crispEdges">' + "".join(out) + "</svg>\n")
    open("site/static/img/ground-hills.svg", "w").write(svg)
    print(len(out), "rects,", len(svg), "bytes")


hills()
