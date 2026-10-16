import type { MockModelRule } from '../helpers/mockModelScript'
import { escapeRegExp } from '../../../src/lib/regexp'
import { codexExecToolCall, codexWaitCellToolCall } from '../helpers/providerToolCalls'

const OUTPUT_TYPE = '^(?:custom_tool_call_output|function_call_output)$'
const CELL = 'Script running with cell ID ([0-9]+)(?:\\r?\\n|$)'
const COMMAND = '"session_id"\\s*:\\s*([0-9]+)'

/** Follow actual yielded IDs instead of assuming that steering finishes a code cell. */
export function codexAsyncExecRules(steeringText?: string): MockModelRule[] {
  const wait = {
    captures: { codeCell: CELL },
    toolCalls: [codexWaitCellToolCall('await-native-cell', '{{codeCell}}')],
  }
  const rules: MockModelRule[] = [
    {
      name: 'codex-await-code-cell',
      priority: 'high',
      when: { protocol: 'openai-responses', lastMessage: { type: OUTPUT_TYPE, text: `^${CELL}` } },
      respond: wait,
    },
    {
      name: 'codex-await-command-session',
      priority: 'high',
      when: { protocol: 'openai-responses', lastMessage: { type: OUTPUT_TYPE, text: COMMAND } },
      respond: {
        captures: { commandSession: COMMAND },
        toolCalls: [codexExecToolCall('await-native-command', [
          'const sessionId = Number("{{commandSession}}");',
          'if (!Number.isSafeInteger(sessionId)) throw new Error("The native command session ID is not a safe integer.");',
          'const result = await tools.write_stdin({ session_id: sessionId, chars: "", yield_time_ms: 10000 });',
          'text(JSON.stringify(result));',
        ].join('\n'))],
      },
    },
  ]
  if (steeringText !== undefined) {
    if (!steeringText.trim())
      throw new Error('The Codex steering continuation requires nonempty text.')
    // A steer follows the yielded cell record as a user message in the next request.
    rules.push({
      name: 'codex-await-steered-cell',
      priority: 'high',
      once: true,
      when: {
        protocol: 'openai-responses',
        body: 'Script running with cell ID [0-9]+',
        lastMessage: { role: 'user', type: '^message$', text: `^${escapeRegExp(steeringText)}$` },
      },
      respond: wait,
    })
  }
  return rules
}
