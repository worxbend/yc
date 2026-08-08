// Package youtube owns YouTube Data API v3 integration for live chat.
//
// It wraps HTTP transport, poll scheduling, quota accounting, target
// resolution, moderation adapters, and event normalization. Package consumers
// receive yc-owned message and state types rather than YouTube JSON shapes, so
// a change in the wire format stays inside this package. Nothing here may
// import Bubble Tea, and every network call takes a context.Context.
package youtube
