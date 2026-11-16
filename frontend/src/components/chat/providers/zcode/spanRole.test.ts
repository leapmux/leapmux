import { describe, expect, it } from 'vitest'
import { ZCODE_TOOL, ZCODE_TOOL_KIND } from '~/generated/contracts/zcode-protocol'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerFor } from '../registry'
import { input } from '../testUtils'
import { zcodeNeedsRequest } from './spanRole'
import './plugin'

function frame(kind: string, fields: Record<string, unknown> = {}, completion?: MessageCompletion) {
  return {
    ...input({
      type: 'tool.updated',
      payload: { kind, toolCallId: 'call', ...fields },
    }, undefined, AgentProvider.ZCODE),
    ...(completion !== undefined
      ? { completion }
      : {}),
  }
}

// A finished frame states no tool name and no input, so its row reads them from the
// `scheduled` frame. A `scheduled` frame never waits for its result.
describe('zcodeNeedsRequest', () => {
  it('is the hook the plugin registers', () => {
    expect(providerFor(AgentProvider.ZCODE)?.transcript.needsRequest).toBe(zcodeNeedsRequest)
  })

  it('answers false for a scheduled frame, also one whose input ZCode omitted', () => {
    expect(zcodeNeedsRequest(frame(ZCODE_TOOL_KIND.Scheduled, {
      toolName: ZCODE_TOOL.Bash,
      input: { command: 'ls' },
    }))).toBe(false)
    expect(zcodeNeedsRequest(frame(ZCODE_TOOL_KIND.Scheduled, { toolName: ZCODE_TOOL.Agent, inputOmitted: true }))).toBe(false)
  })

  it.each([
    ZCODE_TOOL_KIND.Result,
    ZCODE_TOOL_KIND.Error,
    ZCODE_TOOL_KIND.Batch,
  ])('answers true for a %s frame', (kind) => {
    expect(zcodeNeedsRequest(frame(kind, {
      result: { success: true, content: 'ok' },
    }))).toBe(true)
  })

  // A stopped turn stores the call's last frame as its closing row. A `progress` frame
  // states no input, and a `scheduled` frame states its own unless ZCode omitted it.
  it('answers by the input of a retained frame', () => {
    expect(zcodeNeedsRequest(frame(ZCODE_TOOL_KIND.Progress, { stdoutTail: 'so far' }, MessageCompletion.INTERRUPTED))).toBe(true)
    expect(zcodeNeedsRequest(frame(ZCODE_TOOL_KIND.Scheduled, {
      toolName: ZCODE_TOOL.Bash,
      input: { command: 'ls' },
    }, MessageCompletion.INTERRUPTED))).toBe(false)
    expect(zcodeNeedsRequest(frame(ZCODE_TOOL_KIND.Scheduled, { toolName: ZCODE_TOOL.Bash, inputOmitted: true }, MessageCompletion.INTERRUPTED))).toBe(true)
  })

  it('answers false for a batch frame, which states no single call', () => {
    expect(zcodeNeedsRequest(input({
      type: 'tool.updated',
      payload: {
        kind: ZCODE_TOOL_KIND.Batch,
        toolCallIds: ['a', 'b'],
        successCount: 2,
        errorCount: 0,
      },
    }, undefined, AgentProvider.ZCODE))).toBe(false)
  })

  it('answers false for a row outside a tool span', () => {
    expect(zcodeNeedsRequest(input({ type: 'turn.completed', payload: {} }, undefined, AgentProvider.ZCODE))).toBe(false)
    expect(zcodeNeedsRequest(frame(ZCODE_TOOL_KIND.Started, { toolName: ZCODE_TOOL.Bash }))).toBe(false)
  })
})
