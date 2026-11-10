//go:build !devcapture

// Package devbuild reports whether this binary was built with the dev-only
// body-capture paths (DEV_LOG_BODIES / DEV_CAPTURE_FULL_BODIES).
//
// Production builds omit the `devcapture` tag, so BodyCapture is the constant
// false and the compiler drops every branch guarded by it: there is no code in
// the shipped binary that can copy decrypted bodies into an event, whatever the
// environment says. Local development builds with `-tags devcapture`.
package devbuild

// BodyCapture is false in production builds.
const BodyCapture = false
