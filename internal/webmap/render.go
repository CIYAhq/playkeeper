package webmap

import (
	"context"
	"fmt"
)

// Commander runs a console command on one server and returns its output.
type Commander interface {
	Command(ctx context.Context, cmd string) (string, error)
}

// StartDrawing asks squaremap to draw the land explored so far in each
// world, given as dimension ids (a full render); none means the overworld,
// the Nether and the End. Call it once squaremap answers after it was
// installed. Afterwards squaremap draws new land by itself, and it resumes
// an interrupted full render when the server starts again. Asking while a
// world is being drawn does no harm: squaremap says so and carries on.
func StartDrawing(ctx context.Context, c Commander, dimensions ...string) error {
	if len(dimensions) == 0 {
		dimensions = []string{"minecraft:overworld", "minecraft:the_nether", "minecraft:the_end"}
	}
	for _, d := range dimensions {
		if !reDimensionID.MatchString(d) {
			return fail(KindInvalid, kv("field", "dimension", "value", printable(d)),
				fmt.Sprintf("\"%s\" is not a world Playkeeper can ask squaremap to draw.", printable(d)),
				"Worlds are named by their dimension id, such as minecraft:overworld.")
		}
	}
	for _, d := range dimensions {
		if _, err := c.Command(ctx, "squaremap fullrender "+d); err != nil {
			e := fail(KindCommandFailed, kv("world", d),
				"The server did not take Playkeeper's request to draw "+d+".",
				"Check that the server is running, then turn the map on again.")
			e.Err = err
			return e
		}
	}
	return nil
}
