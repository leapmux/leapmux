/**
 * What the approval of a call that starts an agent tells the reader: a subagent
 * (`spawn_agent`), a configured agent of `.cline/agents/`, or a teammate. Cline runs
 * every tool call of such an agent without asking, so approving the call approves each
 * call that the agent makes.
 *
 * A LEAF module: data alone, so the E2E specs read the same words without the plugin's
 * imports, which reach the app's components.
 */
export const CLINE_SPAWN_WARNING = 'Approving lets the agent that this call starts run its tools without asking you, commands and edits included.'
