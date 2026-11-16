package compose

import "regexp"

// Requirement 6.1: no client renders a remote image. A remote image is a
// request to a host that an attacker names, with private text in the URL
// (the "markdown image" exfiltration). Model and tool text can carry one, so
// text that Magi posts into a stream loses its images before it is stored.
// No Magi client renders images today; this keeps it true for a client that
// later renders Markdown or HTML.
//
// Removed: Markdown images (inline and reference style, local or remote) and
// HTML tags that load a resource on their own (img, image, picture, source,
// video, audio, svg, object, embed, iframe). The text around stays readable.
// A Markdown link is not an image and stays: a person has to follow it.
var (
	imgMarkdown = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)|!\[[^\]]*\]\[[^\]]*\]`)
	imgHTML     = regexp.MustCompile(`(?is)<\s*(img|image|picture|source|video|audio|svg|object|embed|iframe)\b[^>]*>?`)
)

// noRemoteImages replaces each image in text with a plain marker.
func noRemoteImages(text string) string {
	if text == "" {
		return text
	}
	text = imgMarkdown.ReplaceAllString(text, "[image removed]")
	return imgHTML.ReplaceAllString(text, "[image removed]")
}
