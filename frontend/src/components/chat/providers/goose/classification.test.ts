import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { renderACPRow } from '../acp/testUtils'
import { providerFor } from '../registry'
import { input } from '../testUtils'
import './plugin'

describe('classifyGooseToolCallUpdate', () => {
  const plugin = providerFor(AgentProvider.GOOSE)!

  // Goose surfaces tool REQUESTS (never results) over ACP via a two-level-nested
  // _meta payload. The backend persists the update to the child transcript as a
  // tool_call_update envelope carrying the _meta; the shared ACP classifier must
  // recognize the shape and route it to a compact "Requested tool: <name>" card
  // rather than hiding it (in_progress tool_call_update) or rendering raw JSON.
  const toolRequestParent = {
    sessionUpdate: 'tool_call_update',
    toolCallId: 'tc-goose',
    status: 'in_progress',
    _meta: {
      toolNotification: {
        type: 'message',
        params: {
          data: {
            type: 'subagent_tool_request',
            subagent_id: 'g-sub-1',
            tool_call: { name: 'Read' },
          },
        },
      },
    },
  }

  it('classifies a subagent_tool_request update as tool_use (not hidden)', () => {
    expect(plugin?.transcript.classify(input(toolRequestParent, undefined, AgentProvider.GOOSE))).toEqual({ kind: 'tool_use' })
  })

  it('renders the requested tool name in a compact card', () => {
    const { container } = renderACPRow(AgentProvider.GOOSE, toolRequestParent)
    expect(container.textContent).toContain('Requested tool: Read')
  })

  it('a plain in_progress tool_call_update (no subagent meta) stays hidden', () => {
    const plain = {
      sessionUpdate: 'tool_call_update',
      toolCallId: 'tc-plain',
      status: 'in_progress',
    }
    expect(plugin?.transcript.classify(input(plain, undefined, AgentProvider.GOOSE))).toEqual({ kind: 'hidden' })
  })
})
