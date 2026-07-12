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

//go:embed sentanyl.js
var sentanylJS []byte

// SentanylJS returns the bytes of the frontend-channel browser SDK, served
// at GET /static/sentanyl.js. packages/js/browser ships the same file for
// npm consumers (kept in sync by its check-sync script).
func SentanylJS() []byte { return sentanylJS }
