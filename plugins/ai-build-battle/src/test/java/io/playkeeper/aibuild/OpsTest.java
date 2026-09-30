package io.playkeeper.aibuild;

import com.google.gson.JsonArray;
import com.google.gson.JsonParser;
import org.bukkit.block.BlockFace;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

class OpsTest {
    private static final Frame PLOT = new Frame(0, 0, 0, BlockFace.NORTH, 30, 80);

    private static Ops.Plan plan(String json) {
        return Ops.plan(JsonParser.parseString(json).getAsJsonArray(), PLOT);
    }

    @Test
    void fillsEveryCellAndHollowOnlyTheShell() {
        assertEquals(27, plan("[{\"op\":\"fill\",\"from\":[0,0,0],\"to\":[2,2,2],\"block\":\"stone\"}]").placements().size());
        assertEquals(26, plan("[{\"op\":\"fill\",\"from\":[0,0,0],\"to\":[2,2,2],\"block\":\"stone\",\"hollow\":true}]").placements().size());
    }

    @Test
    void placesFromTheGroundUpAndRemovesFromTheTopDown() {
        Ops.Plan p = plan("[{\"op\":\"fill\",\"from\":[0,0,0],\"to\":[0,3,0],\"block\":\"stone\"},"
                + "{\"op\":\"remove\",\"from\":[5,0,5],\"to\":[5,3,5]}]");
        for (int i = 1; i < p.placements().size(); i++) {
            assertTrue(p.placements().get(i - 1).y() <= p.placements().get(i).y());
        }
        assertEquals(3, p.removals().get(0).y());
        assertNull(p.removals().get(0).block());
    }

    @Test
    void aLaterOperationOnACellWins() {
        Ops.Plan p = plan("[{\"op\":\"place\",\"at\":[1,0,1],\"block\":\"stone\"},{\"op\":\"place\",\"at\":[1,0,1],\"block\":\"oak_planks\"}]");
        assertEquals(1, p.requested());
        assertEquals("oak_planks", p.placements().get(0).block());
    }

    @Test
    void cellsOutsideThePlotAreCountedNotPlaced() {
        Ops.Plan p = plan("[{\"op\":\"fill\",\"from\":[29,0,0],\"to\":[32,0,0],\"block\":\"stone\"},{\"op\":\"place\",\"at\":[0,-2,0],\"block\":\"stone\"}]");
        assertEquals(2, p.placements().size());
        assertEquals(3, p.skippedOutsidePlot());
    }

    @Test
    void badOperationsAreReportedByNumber() {
        Ops.Plan p = plan("[{\"op\":\"paint\"},{\"op\":\"place\",\"at\":[0,0]},{\"op\":\"fill\",\"from\":[0,0,0],\"to\":[1,1,1]}]");
        assertEquals(3, p.invalid().size());
        assertTrue(p.invalid().get(0).startsWith("op 1: unknown op"));
        assertTrue(p.invalid().get(1).startsWith("op 2:"));
        assertEquals("op 3: no block", p.invalid().get(2));
    }

    @Test
    void aHugeBoxIsRefused() {
        JsonArray ops = JsonParser.parseString("[{\"op\":\"fill\",\"from\":[-1000,0,-1000],\"to\":[1000,300,1000],\"block\":\"stone\"}]").getAsJsonArray();
        Ops.Plan p = Ops.plan(ops, PLOT);
        assertEquals(0, p.requested());
        assertEquals("op 1: too many blocks in one call", p.invalid().get(0));
    }
}
