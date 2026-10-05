import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { renderToolRows } from '~/test-support/toolRowRendering'
import { acpTextContent } from '../acp/testUtils'

import '../testMocks'
import './plugin'

/**
 * The two rows of a refused write that the model STREAMED, as a reader sees them.
 *
 * The request row heads the call: it draws the header with the file. A paired result row
 * draws no header (`ToolMessageLayout`), so only the request row can state the file. The
 * result row draws the refusal. `fast-agent/permission-prompts.spec.ts` reads these two
 * rows by their `data-tool-row-role`.
 *
 * The frames are the frames that Fast Agent 0.10.42 sent to a client that refused the
 * write. A probe captured them from the installed CLI against a local mock of the model.
 * The opening frame states no input. The Worker folds the input of the permission request
 * into the supplement of the stored request row (`conversation.notePermissionToolCall`).
 */
describe('a refused streamed write of fast agent', () => {
  const REFUSAL = 'The user has declined permission to use this tool: acp_filesystem__write_text_file'
  const frames = {
    opening: { sessionUpdate: 'tool_call', toolCallId: 'fast-write', title: 'write_text_file', kind: 'edit', status: 'pending', content: [] },
    ending: { sessionUpdate: 'tool_call_update', toolCallId: 'fast-write', status: 'failed', content: acpTextContent(REFUSAL) },
    spanType: 'edit',
  }
  const supplement = { sessionUpdate: 'tool_call', toolCallId: 'fast-write', status: 'pending', rawInput: { path: '/w/fa-local.txt', content_length: 16 } }

  it('heads the request row with the file that the permission request stated', () => {
    const { request } = renderToolRows(AgentProvider.FAST_AGENT, { ...frames, requestSupplement: supplement })
    expect(request.textContent).toContain('fa-local.txt')
  })

  it('draws the refusal in the result row, which draws no header and so states no file', () => {
    const { result } = renderToolRows(AgentProvider.FAST_AGENT, { ...frames, requestSupplement: supplement })
    expect(result.textContent).toContain(REFUSAL)
    expect(result.textContent).toContain('Declined')
    expect(result.textContent).not.toContain('fa-local.txt')
  })

  // The Worker stored this row before it read the permission request. The request row
  // states the tool word and no file.
  it('heads the request row with the tool word alone when no frame states the file', () => {
    const { request } = renderToolRows(AgentProvider.FAST_AGENT, frames)
    expect(request.textContent).toContain('write_text_file')
    expect(request.textContent).not.toContain('fa-local.txt')
  })
})
