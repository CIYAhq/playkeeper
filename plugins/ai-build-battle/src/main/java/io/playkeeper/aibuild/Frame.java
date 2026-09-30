package io.playkeeper.aibuild;

import org.bukkit.block.BlockFace;
import org.bukkit.block.structure.StructureRotation;

/**
 * A plot's coordinates, as the model sees them, on the world. The plot sits
 * in front of the player who asked, turned so that they look at it from its
 * south-east, as the models' camera does in the videos: the model's "south
 * and east face the viewer" holds whichever way they looked. {@code facing}
 * is where the model's north points on the world.
 */
record Frame(int ox, int oy, int oz, BlockFace facing, int half, int height) {

    Frame {
        if (facing != BlockFace.NORTH && facing != BlockFace.EAST && facing != BlockFace.SOUTH && facing != BlockFace.WEST) {
            throw new IllegalArgumentException("a plot faces north, east, south or west");
        }
    }

    /** The plot {@code dist} blocks ahead of a player at x, z looking along yaw, turned so they see it from its south-east. */
    static Frame inFrontOf(double px, double pz, float yaw, int dist, int oy, int half, int height) {
        return ahead(px, pz, yaw, dist, 0, oy, half, height);
    }

    /**
     * A plot {@code dist} blocks ahead and {@code side} blocks to the right
     * (left when negative) of a player at x, z looking along yaw, turned so
     * they see it from its south-east.
     */
    static Frame ahead(double px, double pz, float yaw, int dist, int side, int oy, int half, int height) {
        double a = Math.toRadians(yaw);
        double lx = -Math.sin(a), lz = Math.cos(a);
        // The player's right is the look direction turned a quarter clockwise, seen from above.
        int cx = (int) Math.floor(px + lx * dist - lz * side);
        int cz = (int) Math.floor(pz + lz * dist + lx * side);
        double vx = px - (cx + 0.5), vz = pz - (cz + 0.5);
        BlockFace north = vx >= 0 ? (vz >= 0 ? BlockFace.NORTH : BlockFace.WEST) : (vz >= 0 ? BlockFace.EAST : BlockFace.SOUTH);
        return new Frame(cx, oy, cz, north, half, height);
    }

    boolean inPlot(int x, int y, int z) {
        return x >= -half && x <= half && z >= -half && z <= half && y >= -1 && y <= height;
    }

    int worldX(int x, int z) {
        return switch (facing) {
            case NORTH -> ox + x;
            case EAST -> ox - z;
            case SOUTH -> ox - x;
            default -> ox + z;
        };
    }

    int worldY(int y) {
        return oy + y;
    }

    int worldZ(int x, int z) {
        return switch (facing) {
            case NORTH -> oz + z;
            case EAST -> oz + x;
            case SOUTH -> oz - z;
            default -> oz - x;
        };
    }

    /** How block states written in the model's frame turn to face the same way on the world. */
    StructureRotation rotation() {
        return switch (facing) {
            case NORTH -> StructureRotation.NONE;
            case EAST -> StructureRotation.CLOCKWISE_90;
            case SOUTH -> StructureRotation.CLOCKWISE_180;
            default -> StructureRotation.COUNTERCLOCKWISE_90;
        };
    }
}
