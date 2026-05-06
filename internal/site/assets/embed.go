// Package assets ships the static JS that activates published pages.
// The Go embed directive requires the asset file to live in the same
// package subtree, so this single-purpose package exists to hold them.
package assets

import _ "embed"

//go:embed sentanyl-video.js
var sentanylVideoJS []byte

// SentanylVideoJS returns the bytes of the runtime player script. The
// caller (marketing-service/handlers) serves these at
// GET /static/sentanyl-video.js.
func SentanylVideoJS() []byte { return sentanylVideoJS }
