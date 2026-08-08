import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { gooseSubagentToolRequestName, isGooseSubagentToolRequest } from '../acp/classification'
import { providerFor } from '../registry'
import { input } from '../testUtils'
import { describeACPStubBasics } from './stubBasics'

import './goose'

const MODE_AUTO = 'auto'

describe('goose provider', () => {
  const plugin = providerFor(AgentProvider.GOOSE)!

  describeACPStubBasics(plugin, { text: true, image: true, pdf: true, binary: true })

  it('uses auto as bypass permission mode', () => {
    expect(plugin.bypassPermissionMode).toBe(MODE_AUTO)
  })

  it('has no plan mode', () => {
    // Goose's writable axis is the top-level permission mode (no plan toggle);
    // the generic settings panel renders the permissionMode group it reports.
    expect(plugin.planMode).toBeUndefined()
  })

  it('still renders the permissionMode group as the trigger mode segment (no plan mode needed)', () => {
    // The trigger mode segment is decoupled from plan mode: Goose has a mode axis
    // (permissionMode) without a plan toggle, so it still declares triggerModeGroupKey.
    expect(plugin.triggerModeGroupKey).toBe('permissionMode')
  })

  it('derives a control-response label via the default ACP permission path (no question hook)', () => {
    // Goose has no question protocol, so it gets the shared acpControlResponseDisplay default.
    expect(plugin.controlResponseDisplay!({
      provider: 'GOOSE',
      requestId: '7',
      request: { method: 'session/request_permission', params: { options: [{ optionId: 'proceed_once', name: 'Allow once' }] } },
      response: { result: { outcome: { optionId: 'proceed_once' } } },
    })).toEqual({ kind: 'label', text: 'Allow once' })
  })

  describe('subagent tool-request', () => {
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
      expect(plugin.classify(input(toolRequestParent, undefined, AgentProvider.GOOSE))).toEqual({
        kind: 'tool_use',
        toolName: 'subagent_tool_request',
        toolUse: toolRequestParent,
        content: [],
      })
    })

    it('renders the requested tool name in a compact card', () => {
      const category = plugin.classify(input(toolRequestParent, undefined, AgentProvider.GOOSE))
      const { container } = render(() => plugin.renderMessage!(category, toolRequestParent))
      expect(container.textContent).toContain('Requested tool: Read')
    })

    it('a plain in_progress tool_call_update (no subagent meta) stays hidden', () => {
      const plain = {
        sessionUpdate: 'tool_call_update',
        toolCallId: 'tc-plain',
        status: 'in_progress',
      }
      expect(plugin.classify(input(plain, undefined, AgentProvider.GOOSE))).toEqual({ kind: 'hidden' })
    })
  })

  describe('isGooseSubagentToolRequest', () => {
    // Each short-circuit arm of the shape detector gets its own case so a
    // regression on one nesting level surfaces precisely.
    it('returns false when _meta is absent', () => {
      expect(isGooseSubagentToolRequest({ sessionUpdate: 'tool_call_update' })).toBe(false)
    })

    it('returns false when toolNotification.type is not "message"', () => {
      expect(isGooseSubagentToolRequest({ _meta: { toolNotification: { type: 'log', params: { data: { type: 'subagent_tool_request' } } } } })).toBe(false)
    })

    it('returns false when the data discriminator is not subagent_tool_request', () => {
      expect(isGooseSubagentToolRequest({ _meta: { toolNotification: { type: 'message', params: { data: { type: 'other' } } } } })).toBe(false)
    })

    it('returns true for the full shape', () => {
      expect(isGooseSubagentToolRequest({ _meta: { toolNotification: { type: 'message', params: { data: { type: 'subagent_tool_request', tool_call: { name: 'Read' } } } } } })).toBe(true)
    })
  })

  describe('gooseSubagentToolRequestName', () => {
    it('extracts the tool_call name', () => {
      const parent = {
        _meta: {
          toolNotification: {
            type: 'message',
            params: { data: { type: 'subagent_tool_request', tool_call: { name: 'Bash' } } },
          },
        },
      }
      expect(gooseSubagentToolRequestName(parent)).toBe('Bash')
    })

    it('returns empty string when the shape is absent', () => {
      expect(gooseSubagentToolRequestName({})).toBe('')
    })
  })
})
