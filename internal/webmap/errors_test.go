package webmap

import (
	"errors"
	"fmt"
	"testing"
)

func TestKindOf(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want Kind
	}{
		{nil, ""},
		{errors.New("plain"), ""},
		{fail(KindNotDrawn, nil, "This part of the map is not drawn yet.", ""), KindNotDrawn},
		{fmt.Errorf("map: %w", fail(KindTooSlow, nil, "squaremap took too long to answer.", "")), KindTooSlow},
	} {
		if got := KindOf(tc.err); got != tc.want {
			t.Errorf("KindOf(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}
