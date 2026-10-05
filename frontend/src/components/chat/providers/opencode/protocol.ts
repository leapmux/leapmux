/** Native fields that only the browser reads. */

/** Native fields for tool result pointers. */
export const OPENCODE_TOOL_OUTPUT_FIELD = {
  Metadata: 'metadata',
  Output: 'output',
  Truncated: 'truncated',
  OutputPath: 'outputPath',
} as const

/**
 * The errors OpenCode writes for a tool call that never ran because the reader or a
 * rule refused it.
 *
 * Each entry is the `message` of an OpenCode error class, byte for byte:
 *
 *   - `PermissionV1.RejectedError` (`packages/core/src/v1/permission.ts`): the Deny
 *     answer.
 *   - `Question.RejectedError` (`packages/opencode/src/question/index.ts`): a dismissed
 *     question. OpenCode's own question tool lets the error fail the call.
 *   - `PermissionV1.CorrectedError`: a Deny answer with the reader's words after the
 *     fixed start. OpenCode's Agent Client Protocol layer replies to a Deny with no
 *     words, so this error does not reach LeapMux today. It stays in the table because
 *     it is one of the three errors of the same permission module.
 *   - `PermissionV1.DeniedError`: a deny rule in the reader's configuration, with the
 *     rules after the fixed start.
 *
 * `OpenCodeFamilyRefusals` in `extractors/toolCall.ts` states why the exact message is
 * the one native signal.
 */
export const OPENCODE_REFUSED_TOOL_ERRORS = {
  messages: [
    'The user rejected permission to use this specific tool call.',
    'The user dismissed this question',
  ],
  messagePrefixes: [
    'The user rejected permission to use this specific tool call with the following feedback: ',
    'The user has specified a rule which prevents you from using this specific tool call. Here are some of the relevant rules ',
  ],
} as const
