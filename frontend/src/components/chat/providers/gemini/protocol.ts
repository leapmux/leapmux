/**
 * Gemini CLI vocabulary that only the frontend interprets.
 *
 * Vocabulary that Go and TypeScript both consume belongs in
 * contracts/gemini-protocol.json. Import the generated values from
 * `~/generated/contracts/gemini-protocol`.
 */

/**
 * The sentence Gemini CLI writes when the reader refuses the permission of a call.
 *
 * Gemini CLI reads the Deny answer as the outcome `cancel`. It then sends a failed update
 * whose one content block is `Tool "<name>" was canceled by the user.`, where `<name>` is
 * the tool's own name, and it records no tool call in its session file (`runTool`,
 * `packages/cli/src/acp/acpSession.ts`). The update carries no refusal field, so this
 * exact sentence is the one native signal. The expression matches the WHOLE text: a
 * tool's own error that quotes the sentence inside a longer one is a failure.
 */
const CANCELED_TOOL_SENTENCE = /^Tool "[^"\n]+" was canceled by the user\.$/

/** Whether one text is exactly the sentence of a call that the reader refused. */
export function isGeminiCanceledToolSentence(text: string): boolean {
  return CANCELED_TOOL_SENTENCE.test(text)
}
