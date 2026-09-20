/**
 * How a subagent's prompt reads: Markdown, or preformatted text.
 *
 * Both sides of a launch carry it. {@link AgentPrompt} is the prompt a child
 * transcript opens with, and `AgentRequest` is the launch that wrote it, so the two
 * must offer the reader the same body. One name keeps them from drifting apart by a
 * word the renderer then does not know.
 */
export type PromptFormat = 'markdown' | 'pre'
