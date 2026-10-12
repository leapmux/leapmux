// Package junie drives JetBrains Junie over the Agent Client Protocol.
//
// Junie's ACP mode is a superset of the base protocol: it carries
// `_meta.jetbrains.air` capabilities, `session/{resume,list,fork,delete,close}`
// and permission-shaped questions. The base answers the standard surface; this
// package adds the launch flags, the custom-model routing, the session store
// under `$JUNIE_HOME/sessions`, and the subagent mapping for
// `nativeSubagentSessions`.
package junie
