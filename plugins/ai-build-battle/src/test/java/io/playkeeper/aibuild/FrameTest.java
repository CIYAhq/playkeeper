package io.playkeeper.aibuild;

import org.bukkit.block.BlockFace;
import org.bukkit.block.structure.StructureRotation;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

class FrameTest {
    /** Ahead of the player, turned so the model's south-east diagonal points back at them. */
    @Test
    void thePlotIsAheadAndSeenFromItsSouthEast() {
        Frame nw = Frame.inFrontOf(0.5, 0.5, 135, 42, 64, 30, 80);
        assertTrue(nw.ox() < -20 && nw.oz() < -20, "looking north-west puts it north-west");
        assertEquals(BlockFace.NORTH, nw.facing());
        assertEquals(BlockFace.SOUTH, Frame.inFrontOf(0.5, 0.5, 315, 42, 64, 30, 80).facing());
        assertEquals(BlockFace.EAST, Frame.inFrontOf(0.5, 0.5, 225, 42, 64, 30, 80).facing());
        assertEquals(BlockFace.WEST, Frame.inFrontOf(0.5, 0.5, 45, 42, 64, 30, 80).facing());
        for (float yaw = 0; yaw < 360; yaw += 7.5f) {
            Frame f = Frame.inFrontOf(10.5, -3.5, yaw, 42, 64, 30, 80);
            int dx = f.worldX(1, 1) - f.ox(), dz = f.worldZ(1, 1) - f.oz();
            double toPlayerX = 10.5 - (f.ox() + 0.5), toPlayerZ = -3.5 - (f.oz() + 0.5);
            assertTrue(dx * toPlayerX >= 0 && dz * toPlayerZ >= 0, "yaw " + yaw);
            assertEquals(42, Math.hypot(toPlayerX, toPlayerZ), 1.5);
        }
    }

    /** Each turn maps the model's axes and block states onto the world. */
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
