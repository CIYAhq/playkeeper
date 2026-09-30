package io.playkeeper.aibuild;

import org.bukkit.ChunkSnapshot;
import org.bukkit.Color;
import org.bukkit.Material;
import org.bukkit.World;
import org.bukkit.block.data.BlockData;

import javax.imageio.ImageIO;
import java.awt.GradientPaint;
import java.awt.Graphics2D;
import java.awt.Polygon;
import java.awt.RenderingHints;
import java.awt.image.BufferedImage;
import java.io.ByteArrayOutputStream;
import java.io.IOException;
import java.util.ArrayList;
import java.util.Base64;
import java.util.HashMap;
import java.util.List;
import java.util.Map;

/**
 * The picture a model gets after each build call, like the videos' camera:
 * the plot from the south-east and a little above, drawn as blocks in their
 * map colours. The chunks are copied on the main thread; drawing happens on
 * the build's own thread.
 */
final class Picture {
    private static final double C = Math.cos(Math.toRadians(30));
    private static final int MAX_SIDE = 1024;

    private Picture() {
    }

    /** Copies of the chunks under the plot. Main thread only. */
    static Map<Long, ChunkSnapshot> capture(World world, Frame f) {
        Map<Long, ChunkSnapshot> out = new HashMap<>();
        for (int x = -f.half(); x <= f.half(); x += 8) {
            for (int z = -f.half(); z <= f.half(); z += 8) {
                addChunk(world, f.worldX(x, z) >> 4, f.worldZ(x, z) >> 4, out);
            }
        }
        for (int[] corner : new int[][]{{-f.half(), -f.half()}, {f.half(), -f.half()}, {-f.half(), f.half()}, {f.half(), f.half()}}) {
            addChunk(world, f.worldX(corner[0], corner[1]) >> 4, f.worldZ(corner[0], corner[1]) >> 4, out);
        }
        return out;
    }

    private static void addChunk(World world, int cx, int cz, Map<Long, ChunkSnapshot> out) {
        out.computeIfAbsent(((long) cx << 32) ^ (cz & 0xffffffffL), k -> world.getChunkAt(cx, cz).getChunkSnapshot(false, false, false));
    }

    /** The plot as a PNG data URL. */
    static String render(Map<Long, ChunkSnapshot> chunks, Frame f, int minY, int maxY) throws IOException {
        int n = 2 * f.half() + 1;
        int ys = f.height() + 2;
        int[] argb = new int[n * ys * n];
        Map<BlockData, Integer> colours = new HashMap<>();
        for (int y = -1; y <= f.height(); y++) {
            int wy = f.worldY(y);
            if (wy < minY || wy >= maxY) {
                continue;
            }
            for (int x = -f.half(); x <= f.half(); x++) {
                for (int z = -f.half(); z <= f.half(); z++) {
                    int wx = f.worldX(x, z), wz = f.worldZ(x, z);
                    ChunkSnapshot s = chunks.get(((long) (wx >> 4) << 32) ^ ((wz >> 4) & 0xffffffffL));
                    if (s == null) {
                        continue;
                    }
                    BlockData d = s.getBlockData(wx & 15, wy, wz & 15);
                    if (d.getMaterial().isAir()) {
                        continue;
                    }
                    argb[idx(f, x, y, z)] = colours.computeIfAbsent(d, Picture::colour);
                }
            }
        }
        return "data:image/png;base64," + Base64.getEncoder().encodeToString(draw(argb, f));
    }

    private static int idx(Frame f, int x, int y, int z) {
        int n = 2 * f.half() + 1;
        return ((y + 1) * n + (x + f.half())) * n + (z + f.half());
    }

    private static int colour(BlockData d) {
        Material m = d.getMaterial();
        String name = m.getKey().getKey();
        Color c;
        try {
            c = d.getMapColor();
        } catch (RuntimeException e) {
            c = Color.fromRGB(0);
        }
        int rgb = c.asRGB();
        if (name.contains("glass")) {
            return (rgb == 0 ? 0x9fd8ee : rgb) | 0x70000000;
        }
        if (rgb == 0) {
            rgb = name.contains("torch") || name.contains("lantern") || name.contains("fire") ? 0xffc34d : 0x8a8a8a;
        }
        return rgb | 0xff000000;
    }

    private static byte[] draw(int[] argb, Frame f) throws IOException {
        int n = 2 * f.half() + 1;
        int ys = f.height() + 2;
        double units = Math.max(2 * n * C, ys + n);
        int s = Math.max(3, (int) Math.floor(MAX_SIDE / units));
        int width = (int) Math.ceil(2 * n * s * C) + 2;
        int height = ys * s + n * s + 2;
        BufferedImage img = new BufferedImage(width, height, BufferedImage.TYPE_INT_RGB);
        Graphics2D g = img.createGraphics();
        g.setRenderingHint(RenderingHints.KEY_ANTIALIASING, RenderingHints.VALUE_ANTIALIAS_OFF);
        g.setPaint(new GradientPaint(0, 0, new java.awt.Color(0x8ec9f5), 0, height, new java.awt.Color(0xdff0fb)));
        g.fillRect(0, 0, width, height);
        double cx = n * s * C + 1;
        double cy = (f.half() + f.height() + 1) * s + 1;
        // Farther cells first: the camera is at +x, +z, above.
        List<int[]> cells = new ArrayList<>();
        for (int y = -1; y <= f.height(); y++) {
            for (int x = -f.half(); x <= f.half(); x++) {
                for (int z = -f.half(); z <= f.half(); z++) {
                    if (argb[idx(f, x, y, z)] != 0) {
                        cells.add(new int[]{x, y, z});
                    }
                }
            }
        }
        cells.sort((a, b) -> a[0] + a[2] != b[0] + b[2] ? Integer.compare(a[0] + a[2], b[0] + b[2]) : a[1] != b[1] ? Integer.compare(a[1], b[1]) : Integer.compare(a[0], b[0]));
        for (int[] c : cells) {
            int x = c[0], y = c[1], z = c[2];
            int col = argb[idx(f, x, y, z)];
            if (!solidAt(argb, f, x, y + 1, z)) {
                face(g, col, 1.0, cx, cy, s, new double[][]{{x, y + 1, z}, {x + 1, y + 1, z}, {x + 1, y + 1, z + 1}, {x, y + 1, z + 1}});
            }
            if (!solidAt(argb, f, x + 1, y, z)) {
                face(g, col, 0.8, cx, cy, s, new double[][]{{x + 1, y, z}, {x + 1, y, z + 1}, {x + 1, y + 1, z + 1}, {x + 1, y + 1, z}});
            }
            if (!solidAt(argb, f, x, y, z + 1)) {
                face(g, col, 0.64, cx, cy, s, new double[][]{{x, y, z + 1}, {x + 1, y, z + 1}, {x + 1, y + 1, z + 1}, {x, y + 1, z + 1}});
            }
        }
        g.dispose();
        // Empty sky above the build only costs the model tokens: keep a band of it.
        int top = height;
        for (int[] c : cells) {
            top = Math.min(top, (int) Math.floor(cy + (c[0] + c[2]) * s * 0.5 - (c[1] + 1) * s));
        }
        int keep = Math.max(0, Math.min(top - 12 * s, height - (int) (width * 0.6)));
        BufferedImage shown = keep > 0 ? img.getSubimage(0, keep, width, height - keep) : img;
        ByteArrayOutputStream out = new ByteArrayOutputStream();
        ImageIO.write(shown, "png", out);
        return out.toByteArray();
    }

    private static boolean solidAt(int[] argb, Frame f, int x, int y, int z) {
        if (x < -f.half() || x > f.half() || z < -f.half() || z > f.half() || y < -1 || y > f.height()) {
            return false;
        }
        int c = argb[idx(f, x, y, z)];
        return c != 0 && (c >>> 24) == 0xff;
    }

    private static void face(Graphics2D g, int argb, double shade, double cx, double cy, int s, double[][] pts) {
        Polygon p = new Polygon();
        for (double[] q : pts) {
            p.addPoint((int) Math.round(cx + (q[0] - q[2]) * s * C), (int) Math.round(cy + (q[0] + q[2]) * s * 0.5 - q[1] * s));
        }
        int a = argb >>> 24;
        int r = (int) (((argb >> 16) & 0xff) * shade), gr = (int) (((argb >> 8) & 0xff) * shade), b = (int) ((argb & 0xff) * shade);
        g.setColor(new java.awt.Color(r, gr, b, a));
        g.fillPolygon(p);
    }
}
