/**
 * How one LAUNCHED thing ended: a subagent run, or the surface a task call asked about.
 *
 * ONE vocabulary, because the two answer the same question. A subagent that finished
 * used to be `completed` and a background task that finished `succeeded`, so a provider
 * author had to learn which of the two surfaces they wrote for -- and the two
 * glyph tables mapped those different words to the same three glyphs, which is the
 * drift this removes.
 *
 * It is NOT {@link ToolRowOutcome}, which says how the CALL ended. That one stays
 * separate on purpose: a call that succeeded can report a task that stopped.
 */
export type RunOutcome = 'completed' | 'failed' | 'running' | 'stopped' | 'unknown'
