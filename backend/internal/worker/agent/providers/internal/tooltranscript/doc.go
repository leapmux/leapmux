// Package tooltranscript wraps the provider services of an agent whose tool
// results live in the session store of its CLI: Cursor, Pi, Reasonix and ZCode.
// The Transcript persists each tool row, reads the stored records through the
// Source of the provider, and enriches the rows with them.
package tooltranscript
