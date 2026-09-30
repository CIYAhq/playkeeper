package io.playkeeper.aibuild;

import org.bukkit.block.BlockFace;
import org.bukkit.block.structure.StructureRotation;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

class FrameTest {
    @Test
    void yawToTheWayThePlayerLooks() {
        assertEquals(BlockFace.SOUTH, Frame.cardinal(0));
        assertEquals(BlockFace.WEST, Frame.cardinal(90));
        assertEquals(BlockFace.NORTH, Frame.cardinal(180));
        assertEquals(BlockFace.NORTH, Frame.cardinal(-180));
        assertEquals(BlockFace.EAST, Frame.cardinal(-90));
        assertEquals(BlockFace.EAST, Frame.cardinal(270));
    }

    /** The model's south (+z) points back at the player, and its east (+x) is on the player's right. */
    @Test
    void theModelsSouthFacesThePlayer() {
        // Player looks east: the plot is east of them, so the model's +z is west and +x is south.
        Frame east = new Frame(100, 64, 0, BlockFace.EAST, 30, 80);
        assertEquals(99, east.worldX(0, 1));
        assertEquals(0, east.worldZ(0, 1));
        assertEquals(100, east.worldX(1, 0));
        assertEquals(1, east.worldZ(1, 0));
        assertEquals(StructureRotation.CLOCKWISE_90, east.rotation());

        Frame north = new Frame(0, 64, -100, BlockFace.NORTH, 30, 80);
        assertEquals(-99, north.worldZ(0, 1));
        assertEquals(1, north.worldX(1, 0));
        assertEquals(StructureRotation.NONE, north.rotation());

        Frame south = new Frame(0, 64, 100, BlockFace.SOUTH, 30, 80);
        assertEquals(99, south.worldZ(0, 1));
        assertEquals(-1, south.worldX(1, 0));
        assertEquals(StructureRotation.CLOCKWISE_180, south.rotation());

        Frame west = new Frame(-100, 64, 0, BlockFace.WEST, 30, 80);
        assertEquals(-99, west.worldX(0, 1));
        assertEquals(-1, west.worldZ(1, 0));
        assertEquals(StructureRotation.COUNTERCLOCKWISE_90, west.rotation());
        assertEquals(74, west.worldY(10));
    }

    @Test
    void thePlotIncludesTheGroundLayer() {
        Frame f = new Frame(0, 0, 0, BlockFace.NORTH, 30, 80);
        assertTrue(f.inPlot(30, -1, -30));
        assertTrue(f.inPlot(0, 80, 0));
        assertFalse(f.inPlot(31, 0, 0));
        assertFalse(f.inPlot(0, -2, 0));
        assertFalse(f.inPlot(0, 81, 0));
    }
}
