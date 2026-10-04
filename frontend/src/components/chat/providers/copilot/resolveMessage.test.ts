import { describe, expect, it } from 'vitest'
import { COPILOT_EVENT } from '~/generated/contracts/copilot-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '~/lib/jsonPick'
import { resolveMessageForRendering } from '../registry'
import { copilotEventData } from './protocol'

import './plugin'

function completion(): Record<string, unknown> {
  return {
    jsonrpc: '2.0',
    method: 'session.event',
    params: {
      sessionId: 'session-1',
      event: {
        id: 'complete-1',
        type: COPILOT_EVENT.ToolCompleted,
        data: {
          toolCallId: 'view-1',
          success: true,
          result: {
            content: 'Viewed image file successfully.',
            binaryResultsForLlm: [{ type: 'image', data: 'iVBORw0KGgo=', mimeType: 'image/png' }],
          },
        },
      },
    },
  }
}

function resolvedOf(original: Record<string, unknown>) {
  return resolveMessageForRendering({
    rawText: '',
    topLevel: original,
    parentObject: original,
    wrapper: null,
  }, AgentProvider.GITHUB_COPILOT)
}

describe('copilot binary asset resolution', () => {
  it('adds the live completion image bytes to the tool result for rendering', () => {
    const original = completion()
    const before = JSON.stringify(original)
    const resolved = resolvedOf(original)
    const event = copilotEventData(resolved.parentObject, COPILOT_EVENT.ToolCompleted)
    const result = isObject(event?.result) ? event.result : undefined
    expect(result?.contents).toEqual([{ type: 'image', data: 'iVBORw0KGgo=', mimeType: 'image/png' }])
    expect(JSON.stringify(original)).toBe(before)
  })

  it('keeps a persisted asset reference without inline bytes as text only', () => {
    const original = completion()
    const event = copilotEventData(original, COPILOT_EVENT.ToolCompleted)
    const result = isObject(event?.result) ? event.result : undefined
    if (!result)
      throw new Error('the fixture has no tool result')
    result.binaryResultsForLlm = [{ type: 'image', assetId: 'sha256:png', mimeType: 'image/png', byteLength: 8 }]

    const resolved = resolvedOf(original)
    expect(resolved.parentObject).toBe(original)
  })

  it('does not repeat an image already present in native contents', () => {
    const original = completion()
    const event = copilotEventData(original, COPILOT_EVENT.ToolCompleted)
    const result = isObject(event?.result) ? event.result : undefined
    if (!result)
      throw new Error('the fixture has no tool result')
    result.contents = [{ type: 'image', data: 'iVBORw0KGgo=', mimeType: 'image/png' }]

    const resolved = resolvedOf(original)
    expect(resolved.parentObject).toBe(original)
    expect(result.contents).toHaveLength(1)
  })

  it('ignores empty image data, other binary resources, and a wrong MIME type', () => {
    const original = completion()
    const event = copilotEventData(original, COPILOT_EVENT.ToolCompleted)
    const result = isObject(event?.result) ? event.result : undefined
    if (!result)
      throw new Error('the fixture has no tool result')
    result.binaryResultsForLlm = [
      { type: 'image', data: '', mimeType: 'image/png' },
      { type: 'resource', data: 'aGk=', mimeType: 'application/octet-stream' },
      { type: 'image', data: 'aGk=', mimeType: 'application/pdf' },
    ]

    const resolved = resolvedOf(original)
    expect(resolved.parentObject).toBe(original)
  })
})
