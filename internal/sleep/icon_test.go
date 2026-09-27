package sleep

import (
	"encoding/base64"
	"errors"
	"testing"
)

func TestIconURI(t *testing.T) {
	b := iconPNG(t, 64, 64)
	got, err := IconURI(b)
	if err != nil || got != iconPrefix+base64.StdEncoding.EncodeToString(b) {
		t.Fatalf("got %.40q…, %v", got, err)
	}
	if st := (Status{Icon: got}).clean(); st.Icon != got {
		t.Fatal("Status dropped an icon IconURI returned")
	}
}

func TestIconURIRefuses(t *testing.T) {
	cases := []struct {
		name string
		b    []byte
		code string
	}{
		{"wrong size", iconPNG(t, 32, 32), "icon_invalid"},
		{"not a PNG", []byte("GIF89a not a png at all"), "icon_invalid"},
		{"empty", nil, "icon_invalid"},
		{"over 20 KiB", append(iconPNG(t, 64, 64), make([]byte, MaxIconBytes)...), "icon_too_large"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := IconURI(c.b)
			var e *Error
			if !errors.As(err, &e) || e.Code != c.code || got != "" {
				t.Fatalf("got %.40q, %#v, want code %s", got, err, c.code)
			}
			if e.Msg == "" {
				t.Fatal("no message")
			}
		})
	}
}
