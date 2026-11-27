// Package limitfit learns, per user, how much each kind of token moves Claude Code's plan-usage
// meters (the 5-hour and weekly limits), from the meter readings the status line records
// (limit_samples) and this machine's Claude Code history (history_requests). It is a per-user
// estimate from the documented meter, never an official weighting. See docs/limit-fit.md.
//
// Method in short: readings in one reset window are paired into intervals, merged until the meter
// moved enough to outweigh its rounding; each interval's tokens are summed by type and model family;
// non-negative weights are fitted by least squares, trimming intervals the tokens cannot explain
// (outside drain: claude.ai, other machines, hidden calls); a block bootstrap over reset windows gives
// 90% intervals. Gates hold everything back until there is enough data, and a rolling check drops
// data from before a change in how the meter counts.
//
// Pure Go, deterministic: the same data gives the same result whatever order it arrives in.
package limitfit
