// Package limits holds shared local transport defaults.
package limits

// MessageBytes is a ceiling, not a preallocated buffer. Hosts may override it
// through their transport options independently of provider payload limits.
const MessageBytes = 128 << 20
