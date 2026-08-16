// Package youtube owns YouTube Data API v3 integration for live chat.
//
// It wraps HTTP transport, poll scheduling, target resolution, moderation
// adapters, and event normalization. Quota accounting lives next door in
// internal/quota - this package charges that ledger for every request it
// dispatches and asks it for the budget floor, but it does not own the cost
// table or the persisted records. Package consumers
// receive yc-owned message and state types rather than YouTube JSON shapes, so
// a change in the wire format stays inside this package. Nothing here may
// import Bubble Tea, and every network call takes a context.Context.
package youtube
