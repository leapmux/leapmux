/**
 * Kilo vocabulary that only the frontend interprets.
 *
 * Kilo shares OpenCode's wire format, but not every error. Each table here is Kilo's
 * own, and the OpenCode family adapter reads it beside OpenCode's.
 */

/**
 * The errors Kilo writes for a tool call that never ran because the reader or a rule
 * refused it.
 *
 * Each entry is the `message` of a Kilo error class in `packages/core/src/v1/permission.ts`,
 * byte for byte:
 *
 *   - `PermissionV1.RejectedError`: the Deny answer.
 *   - `PermissionV1.CorrectedError`: a Deny answer with the reader's words after the
 *     fixed start. Kilo's Agent Client Protocol layer replies to a Deny with no words,
 *     so this error does not reach LeapMux today. It stays in the table because it is
 *     one of the three errors of the same permission module.
 *   - `PermissionV1.DeniedError`: a deny rule in the reader's configuration, with the
 *     rules after the fixed start.
 *
 * OpenCode's dismissed-question error is absent on purpose. Kilo's question tool
 * catches that error and answers the call as COMPLETED
 * (`KiloQuestionTool.dismissedResult`, `packages/opencode/src/kilocode/tool/question.ts`),
 * and Kilo's `plan_exit` asks no question.
 *
 * `OpenCodeFamilyRefusals` in `opencode/extractors/toolCall.ts` states why the exact
 * message is the one native signal.
 */
export const KILO_REFUSED_TOOL_ERRORS = {
  messages: [
    'The user rejected permission to use this specific tool call.',
  ],
  messagePrefixes: [
    'The user rejected permission to use this specific tool call with the following feedback: ',
    'The user has specified a rule which prevents you from using this specific tool call. Here are some of the relevant rules ',
  ],
} as const
