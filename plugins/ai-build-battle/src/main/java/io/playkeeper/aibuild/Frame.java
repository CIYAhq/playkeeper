package io.playkeeper.aibuild;

import org.bukkit.block.BlockFace;
import org.bukkit.block.structure.StructureRotation;

/**
 * A plot's coordinates, as the model sees them, on the world. The plot sits
 * in front of the player who asked, and its south (+z) side faces them, so
 * the model's "south and east face the viewer" holds whichever way they
 * looked: {@code facing} is the way from the player to the plot, and the
 * model's north points that way.
 */
record Frame(int ox, int oy, int oz, BlockFace facing, int half, int height) {

    Frame {
        if (facing != BlockFace.NORTH && facing != BlockFace.EAST && facing != BlockFace.SOUTH && facing != BlockFace.WEST) {
            throw new IllegalArgumentException("a plot faces north, east, south or west");
        }
    }

    /** The cardinal direction a player with this yaw looks towards. */
    static BlockFace cardinal(float yaw) {
        float y = ((yaw % 360) + 360) % 360;
        if (y >= 45 && y < 135) {
            return BlockFace.WEST;
        }
        if (y >= 135 && y < 225) {
            return BlockFace.NORTH;
        }
        if (y >= 225 && y < 315) {
            return BlockFace.EAST;
        }
        return BlockFace.SOUTH;
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
