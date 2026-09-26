package webmap

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

type fakeConsole struct {
	sent   []string
	refuse string
	err    error
}

func (c *fakeConsole) Command(_ context.Context, cmd string) (string, error) {
	c.sent = append(c.sent, cmd)
	if cmd == c.refuse {
		return "", c.err
	}
	return "", nil
}

func TestStartDrawingDrawsTheThreeVanillaWorlds(t *testing.T) {
	c := &fakeConsole{}
	if err := StartDrawing(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"squaremap fullrender minecraft:overworld",
		"squaremap fullrender minecraft:the_nether",
		"squaremap fullrender minecraft:the_end",
	}
	if !slices.Equal(c.sent, want) {
		t.Errorf("sent %q", c.sent)
	}
}

func TestStartDrawingDrawsTheWorldsItIsGiven(t *testing.T) {
	longest := strings.Repeat("n", 64) + ":" + strings.Repeat("p", 128)
	c := &fakeConsole{}
	if err := StartDrawing(context.Background(), c, "terralith:overworld", "minecraft:creative", longest); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"squaremap fullrender terralith:overworld",
		"squaremap fullrender minecraft:creative",
		"squaremap fullrender " + longest,
	}
	if !slices.Equal(c.sent, want) {
		t.Errorf("sent %q", c.sent)
	}
}

func TestStartDrawingRefusesWhatIsNotADimensionID(t *testing.T) {
	for _, d := range []string{
		"",
		"overworld",
		"minecraft:",
		":overworld",
		"Minecraft:Overworld",
		"minecraft:overworld extra",
		"minecraft:overworld\nop Notch",
		"minecraft:overworld;op Notch",
		"minecraft:over world",
		strings.Repeat("n", 65) + ":overworld",
		"minecraft:" + strings.Repeat("p", 129),
		"minecraft:overworld:again",
		"§cminecraft:overworld",
	} {
		c := &fakeConsole{}
		err := StartDrawing(context.Background(), c, "minecraft:overworld", d)
		var e *Error
		if !errors.As(err, &e) || e.Kind != KindInvalid || e.Params["field"] != "dimension" || e.Params["value"] != printable(d) {
			t.Errorf("%q: got %#v", d, err)
			continue
		}
		if len(c.sent) != 0 {
			t.Errorf("%q: sent %q before refusing", d, c.sent)
		}
		if e.Hint == "" || strings.ContainsAny(e.Msg, "\n\r") {
			t.Errorf("%q: message %q, hint %q", d, e.Msg, e.Hint)
		}
	}
}

func TestStartDrawingStopsWhenTheServerRefuses(t *testing.T) {
	cause := errors.New("rcon: password secret-hunter2 was refused")
	c := &fakeConsole{refuse: "squaremap fullrender minecraft:the_nether", err: cause}
	err := StartDrawing(context.Background(), c)
	var e *Error
	if !errors.As(err, &e) || e.Kind != KindCommandFailed || e.Params["world"] != "minecraft:the_nether" || !errors.Is(err, cause) {
		t.Fatalf("got %#v", err)
	}
	if strings.Contains(e.Msg+e.Hint, "secret") {
		t.Errorf("the cause is in message %q, hint %q", e.Msg, e.Hint)
	}
	if len(c.sent) != 2 {
		t.Errorf("sent %q", c.sent)
	}
}
