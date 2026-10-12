import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { renderToolRows } from '~/test-support/toolRowRendering'
import { acpTextContent } from '../acp/testUtils'

import '../testMocks'
import './plugin'

/**
 * The two rows of a refused `write_file`, as a reader sees them.
 *
 * Gemini CLI 0.62.0 opens the call with the proposed diff and the file in `locations`. It
 * states no raw input. The failed update replaces the diff with the refusal and states no
 * file. A probe captured these frames from the installed CLI against a local mock of the
 * model. The request row heads the call with the file. A paired result row draws no header
 * (`ToolMessageLayout`). `gemini-cli/permission-prompts.spec.ts` reads these two rows by
 * their `data-tool-row-role`.
 */
describe('a refused write of gemini cli', () => {
  const REFUSAL = 'Tool "write_file" was canceled by the user.'
  const frames = {
    opening: {
      sessionUpdate: 'tool_call',
      toolCallId: 'write_file__denied',
      status: 'pending',
      title: 'Writing to native-denied-file-write.txt',
      kind: 'edit',
      content: [{ type: 'diff', path: '/w/native-denied-file-write.txt', oldText: 'KEEP_THE_NATIVE_FILE\n', newText: 'PROPOSED_NATIVE_TEXT\n', _meta: { kind: 'modify' } }],
      locations: [{ path: '/w/native-denied-file-write.txt' }],
    },
    ending: { sessionUpdate: 'tool_call_update', toolCallId: 'write_file__denied', status: 'failed', kind: 'edit', content: acpTextContent(REFUSAL) },
    spanType: 'edit',
  }

  it('heads the request row with the file and draws no proposed text', () => {
    const { request } = renderToolRows(AgentProvider.GEMINI_CLI, frames)
    expect(request.textContent).toContain('native-denied-file-write.txt')
    expect(request.textContent).not.toContain('PROPOSED_NATIVE_TEXT')
  })

  it('draws the refusal in the result row, which draws no header and so states no file', () => {
    const { result } = renderToolRows(AgentProvider.GEMINI_CLI, frames)
    expect(result.textContent).toContain(REFUSAL)
    expect(result.textContent).toContain('Declined')
    expect(result.textContent).not.toContain('native-denied-file-write.txt')
    expect(result.textContent).not.toContain('PROPOSED_NATIVE_TEXT')
  })
})
