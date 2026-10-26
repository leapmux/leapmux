/**
 * Fast Agent vocabulary that only the frontend interprets.
 *
 * Vocabulary that Go and TypeScript both consume belongs in
 * contracts/fastagent-protocol.json. Import the generated values from
 * `~/generated/contracts/fastagent-protocol`.
 */

/**
 * The sentence Fast Agent writes when the reader refuses the permission of a call.
 *
 * Fast Agent asks through `session/request_permission`. Its permission adapter
 * (`ACPToolPermissionAdapter.check_permission`) reads a Deny answer as a rejection and
 * writes `The user has declined permission to use this tool: <server>__<tool>`. For a
 * rejection that it remembers, the sentence says `permanently declined`. Fast Agent
 * then sends a failed update whose one content block is that sentence
 * (`ACPToolProgressManager.on_tool_permission_denied`). The update carries no refusal
 * field, so this exact sentence is the one native signal.
 *
 * The expression matches the WHOLE text, and the tool name holds no whitespace. A tool's
 * own error that quotes the sentence inside a longer text is a failure.
 */
const REFUSAL_SENTENCE = /^The user has (?:permanently )?declined permission to use this tool: \S+$/

/** Native shell result metadata and complete retained-file notices. */
export const FAST_AGENT_SHELL = {
  Metadata: 'fast-agent-shell-process-metadata',
  ExitCode: 'exit_code',
  OutputNotice: /^\[Output truncated: showing first \d+ bytes and last \d+ bytes of \d+ bytes \(~\d+ of ~\d+ tokens\); omitted \d+ middle bytes\. (?:The complete output is available during this session at ([^\r\n]+?)\.|The first \d+ bytes of the output are available during this session at ([^\r\n]+?); the temporary-file quota was reached\.) Use read_text_file for selected line ranges or run a targeted search against that file; avoid reading the entire file unless necessary\.\]$/gmu,
  OutputPath: /(?:^|[/\\])fast-agent-output-[\w-]+[/\\]output-(?:0|[1-9]\d*)\.log$/u,
} as const

/** Whether one text is exactly the sentence of a call that the reader refused. */
export function isFastAgentRefusalSentence(text: string): boolean {
  return REFUSAL_SENTENCE.test(text)
}
