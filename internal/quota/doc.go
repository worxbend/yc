// Package quota owns yc's estimated YouTube Data API quota accounting.
//
// It holds the per-endpoint cost table, the daily ledger that tallies spend
// against the Pacific-midnight reset, the on-disk records that carry that tally
// across restarts, and the budget arithmetic that turns remaining units into a
// poll cadence. Nothing here speaks HTTP or knows a YouTube JSON shape - it is
// given an endpoint name and hands back units, so internal/youtube imports this
// package and never the other way around.
//
// It is separate from internal/youtube because it changes for different
// reasons. Quota policy moves when Google republishes a cost, when the reset
// boundary is questioned, or when a user overrides a table in config; the
// transport next door moves when the wire format does. Keeping them apart means
// a quota fix does not churn the package that parses chat messages, and the one
// consumer that only wants the meter - `yc quota` in internal/cli - can depend
// on this alone.
//
// Every unit figure this package produces is an estimate. Google publishes no
// quota cost for any live chat method, so the numbers the poll budget rests on
// are community-observed and config-overridable. Snapshot.Estimated carries
// that fact to every surface that prints one.
package quota
