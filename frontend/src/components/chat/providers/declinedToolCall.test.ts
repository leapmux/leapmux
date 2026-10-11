import type { ToolCallEnvelope, ToolCallSpec } from '../model/toolCall'
import { describe, expect, it } from 'vitest'
import { createToolCall } from '../model/createToolCall'
import { failedResult, proseResult } from '../model/toolCall'
import { declinedToolCallSpec } from './declinedToolCall'

/** The envelope of a call whose frame states that it failed, as a refused call's frame does. */
const FAILED_ENVELOPE: ToolCallEnvelope = {
  id: 'refused-call',
  name: 'bash',
  lifecycle: { frameStatus: 'failed', providerOutcome: null, retainedOutcome: null, rowFinal: true, resultFrameLanded: true },
}

const PICTURE = { mimeType: 'image/png', data: 'aGk=' }

describe('declinedToolCallSpec', () => {
  it('replaces the result a builder read with the refusal, and states the declined status', () => {
    const spec: ToolCallSpec = {
      kind: 'execute',
      request: { command: 'rm -f doomed.txt' },
      result: { commands: [{ output: 'The user rejected this tool call.' }], unresolvedTerminals: [] },
    }
    expect(declinedToolCallSpec(spec, 'The user rejected this tool call.')).toStrictEqual({
      kind: 'execute',
      request: { command: 'rm -f doomed.txt' },
      images: [],
      statusOverride: 'declined',
      result: failedResult('The user rejected this tool call.'),
    })
  })

  // The model admits no artifact on a refused call, so a builder that attached one
  // would degrade the whole row to the uncategorized card.
  it('drops every artifact the builder attached', () => {
    const spec: ToolCallSpec = {
      kind: 'read',
      request: { path: '/p/a.png' },
      images: [PICTURE],
      extraContent: [{ type: 'text', text: 'stale block' }],
      truncated: true,
    }
    const declined = declinedToolCallSpec(spec, 'Refused.')
    expect(declined).not.toHaveProperty('extraContent')
    expect(declined).not.toHaveProperty('truncated')
    expect(declined.images).toStrictEqual([])
  })

  // The refusal changes the outcome, never the identity of the call it refuses.
  it('keeps the kind, the request and the labels of the call', () => {
    const spec: ToolCallSpec = {
      kind: 'switch_mode',
      name: 'exit_plan_mode',
      label: 'Plan',
      title: 'Exit plan mode',
      metadata: [{ label: 'Mode', value: 'build' }],
      request: { mode: 'build', declinedTitle: 'Plan sent back' },
      result: proseResult('Switched to build mode.'),
    }
    expect(declinedToolCallSpec(spec, 'Plan not approved.')).toMatchObject({
      kind: 'switch_mode',
      name: 'exit_plan_mode',
      label: 'Plan',
      title: 'Exit plan mode',
      metadata: [{ label: 'Mode', value: 'build' }],
      request: { mode: 'build', declinedTitle: 'Plan sent back' },
      result: failedResult('Plan not approved.'),
    })
  })

  it('states a refusal that carried no words as an empty failure', () => {
    expect(declinedToolCallSpec({ kind: 'fetch', request: { url: 'https://example.com' } }, '').result).toStrictEqual(failedResult(''))
  })

  // The builder is the one judge of a valid call. A refused call whose frame carried
  // artifacts must still build as itself, under the declined status.
  it('builds a declined call that the model accepts, whatever the frame carried', () => {
    const spec: ToolCallSpec = {
      kind: 'read',
      request: { path: '/p/a.png' },
      images: [PICTURE],
      extraContent: [{ type: 'text', text: 'stale block' }],
      result: { lines: null, fallbackContent: 'stale' },
    }
    const call = createToolCall(FAILED_ENVELOPE, declinedToolCallSpec(spec, 'Refused.'))
    expect(call.kind).toBe('read')
    expect(call.status).toBe('declined')
    expect(call.result).toStrictEqual(failedResult('Refused.'))
    expect(call.images).toStrictEqual([])
    expect(call.degradation).toBeUndefined()
  })

  // A frame the retained turn marked as interrupted still states its own refusal: the
  // call never ran, whatever happened to the turn around it.
  it('outranks the outcome the turn retained', () => {
    const call = createToolCall(
      { ...FAILED_ENVELOPE, lifecycle: { ...FAILED_ENVELOPE.lifecycle, retainedOutcome: 'interrupted' } },
      declinedToolCallSpec({ kind: 'execute', request: { command: 'ls' } }, 'Refused.'),
    )
    expect(call.status).toBe('declined')
  })
})
