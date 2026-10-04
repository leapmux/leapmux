// Package dirac drives Dirac over the Agent Client Protocol.
//
// Dirac speaks ACP with `dev.dirac/*` extensions: steering on
// `dev.dirac/whisper`, status on `dev.dirac/steering_status`, and compaction
// events on `dev.dirac/pinned_messages_update`. The turn ends only through the
// `respond` tool's `complete` operation, and `session/list` and
// `session/resume` are unregistered in the releases this was written against,
// so the session picker reads Dirac's own task-history store and a resume goes
// through `session/load`.
package dirac
