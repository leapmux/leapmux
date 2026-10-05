/** Native values that only the Qwen browser readers consume. */
export const QWEN_SHELL_RESULT = 'shell_result'
export const QWEN_QUESTION_ANSWERS = 'ask_user_question_answers'
export const QWEN_OUTPUT_FILES = 'outputFiles'
export const QWEN_OUTPUT_NOTICE_PREFIX = 'Tool output was too large and has been truncated.\nThe full output has been saved to: '

/**
 * The sentence that Qwen Code writes when the reader refuses the permission of a call.
 *
 * Qwen Code reads the Deny answer as the outcome `cancel`. It then sends a failed update.
 * The update has one content block: `Tool "<name>" was canceled by the user.` Here
 * `<name>` is the name of the tool (`stopAfterPermissionCancel` in its ACP session,
 * 0.24.7). The update has no refusal field, so this exact sentence is the only native
 * signal. The expression matches the WHOLE text. A tool error that quotes the sentence
 * inside a longer text is a failure.
 */
const CANCELED_TOOL_SENTENCE = /^Tool "[^"\n]+" was canceled by the user\.$/

/** Whether one text is exactly the sentence of a call that the reader refused. */
export function isQwenCanceledToolSentence(text: string): boolean {
  return CANCELED_TOOL_SENTENCE.test(text)
}
