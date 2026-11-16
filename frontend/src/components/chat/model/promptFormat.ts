/**
 * How a subagent's prompt reads: Markdown, or preformatted text.
 *
 * The launch of a subagent (`AgentRequest`) carries it, and the renderer of the
 * launch draws the prompt by it. Each provider that states a format uses this one
 * type, so no provider can state a word that the renderer does not know.
 */
export type PromptFormat = 'markdown' | 'pre'
