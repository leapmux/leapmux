import type { MiMoToolPart } from './toolCommon'
import { describe, expect, it } from 'vitest'
import { MIMO_EVENT, MIMO_PART_TYPE, MIMO_TOOL, MIMO_TOOL_STATUS } from '~/generated/contracts/mimo-protocol'
import { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { compactionFrame, mimoFrame, statusFrame, TEST_SESSION, toolFrame } from '~/test-support/mimoFixtures'
import { mimoEvent, mimoPart, mimoToolFinished, mimoToolImages, mimoToolPart, mimoToolSpanRole } from './toolCommon'

/** A tool part frame whose `part` is exactly the given object. */
function partFrame(part: unknown): Record<string, unknown> {
  return mimoFrame(MIMO_EVENT.MessagePartUpdated, { sessionID: TEST_SESSION, part })
}

/** One tool part, with the fields a test does not state left at their empty values. */
function toolPart(fields: Partial<MiMoToolPart>): MiMoToolPart {
  return { callId: 'call-1', tool: MIMO_TOOL.Bash, status: MIMO_TOOL_STATUS.Completed, input: {}, output: '', error: '', title: '', metadata: {}, attachments: [], ...fields }
}

describe('mimoEvent', () => {
  it('reads the type and the properties of a stored event', () => {
    expect(mimoEvent(statusFrame('idle'))).toEqual({ type: MIMO_EVENT.SessionStatus, properties: { sessionID: TEST_SESSION, status: { type: 'idle' } } })
  })

  it.each([
    ['null', null],
    ['a string', 'session.status'],
    ['a list', [{ type: 'session.status', properties: {} }]],
    ['an event with no type', { properties: {} }],
    ['an event with an empty type', { type: '', properties: {} }],
    ['an event with no properties', { type: 'session.status' }],
    ['an event whose properties are a list', { type: 'session.status', properties: [] }],
    ['a LeapMux user row', { content: 'hi' }],
  ])('reads no event from %s', (_name, parsed) => {
    expect(mimoEvent(parsed)).toBeNull()
  })
})

describe('mimoPart', () => {
  it('reads the part of a part update', () => {
    expect(mimoPart(compactionFrame(false))).toMatchObject({ type: MIMO_PART_TYPE.Compaction })
  })

  it('reads no part from an event of another type', () => {
    expect(mimoPart(mimoFrame(MIMO_EVENT.SessionStatus, { part: { type: MIMO_PART_TYPE.Tool } }))).toBeNull()
  })

  it('reads no part from a part update that states none', () => {
    expect(mimoPart(mimoFrame(MIMO_EVENT.MessagePartUpdated, { sessionID: TEST_SESSION }))).toBeNull()
    expect(mimoPart(partFrame('tool'))).toBeNull()
  })
})

describe('mimoToolPart', () => {
  it('reads every field of a finished call', () => {
    const frame = toolFrame(MIMO_TOOL.Bash, {
      input: { command: 'ls' },
      output: 'a.ts\n',
      title: 'List',
      metadata: { exit: 0 },
      attachments: [{ type: 'file', url: 'data:image/png;base64,AA==' }],
    })
    expect(mimoToolPart(frame)).toEqual({
      callId: 'call-1',
      tool: MIMO_TOOL.Bash,
      status: MIMO_TOOL_STATUS.Completed,
      input: { command: 'ls' },
      output: 'a.ts\n',
      error: '',
      title: 'List',
      metadata: { exit: 0 },
      attachments: [{ type: 'file', url: 'data:image/png;base64,AA==' }],
    })
  })

  // A state that states only its status reads every other field as empty, so each
  // reader handles one shape rather than an absent field.
  it('reads each absent or malformed field as empty', () => {
    const frame = partFrame({ type: MIMO_PART_TYPE.Tool, callID: 'call-9', state: { status: 'running', input: 'ls', metadata: [], attachments: 'none' } })
    expect(mimoToolPart(frame)).toEqual({ callId: 'call-9', tool: '', status: 'running', input: {}, output: '', error: '', title: '', metadata: {}, attachments: [] })
  })

  it('keeps only the attachments that are objects', () => {
    const frame = toolFrame(MIMO_TOOL.Read, { attachments: [{ url: 'a' }, 'b', null, [1], { url: 'c' }] as unknown as Record<string, unknown>[] })
    expect(mimoToolPart(frame)?.attachments).toEqual([{ url: 'a' }, { url: 'c' }])
  })

  it.each([
    ['a compaction part', compactionFrame(true)],
    ['a text part', partFrame({ type: 'text', callID: 'call-1', state: {} })],
    ['a tool part with no call id', partFrame({ type: MIMO_PART_TYPE.Tool, state: { status: 'completed' } })],
    ['a tool part with an empty call id', partFrame({ type: MIMO_PART_TYPE.Tool, callID: '', state: { status: 'completed' } })],
    ['a tool part with no state', partFrame({ type: MIMO_PART_TYPE.Tool, callID: 'call-1' })],
    ['a status event', statusFrame('busy')],
  ])('reads no tool part from %s', (_name, frame) => {
    expect(mimoToolPart(frame)).toBeNull()
  })
})

describe('mimoToolFinished', () => {
  // `suspended` stands for a status that a later MiMo release can add.
  it.each([
    [true, 'the completed status', MIMO_TOOL_STATUS.Completed],
    [true, 'the error status', MIMO_TOOL_STATUS.Error],
    [false, 'the running status', MIMO_TOOL_STATUS.Running],
    [false, 'the pending status', MIMO_TOOL_STATUS.Pending],
    [false, 'an empty status', ''],
    [false, 'a status of a later release', 'suspended'],
  ])('answers %s for %s', (finished, _name, status) => {
    expect(mimoToolFinished({ status })).toBe(finished)
  })
})

describe('mimoToolSpanRole', () => {
  it('reads a finished call as the result and a running one as the request', () => {
    expect(mimoToolSpanRole(toolPart({ status: MIMO_TOOL_STATUS.Completed }), undefined)).toBe('result')
    expect(mimoToolSpanRole(toolPart({ status: MIMO_TOOL_STATUS.Error }), undefined)).toBe('result')
    expect(mimoToolSpanRole(toolPart({ status: MIMO_TOOL_STATUS.Running }), undefined)).toBe('request')
  })

  // The worker persists no pending frame, because it states no input yet.
  it('reads a pending call and a status it does not know as outside a span', () => {
    expect(mimoToolSpanRole(toolPart({ status: MIMO_TOOL_STATUS.Pending }), undefined)).toBe('other')
    expect(mimoToolSpanRole(toolPart({ status: '' }), undefined)).toBe('other')
  })

  // The last update of a call that the turn cut short still reads as running, and
  // LeapMux's completion column ends the span with it.
  it.each([
    MessageCompletion.INTERRUPTED,
    MessageCompletion.ERROR,
    MessageCompletion.COMPLETE,
  ])('reads a running frame with the completion %s as the result', (completion) => {
    expect(mimoToolSpanRole(toolPart({ status: MIMO_TOOL_STATUS.Running }), completion)).toBe('result')
  })

  it('keeps a running frame the request when the completion states nothing', () => {
    expect(mimoToolSpanRole(toolPart({ status: MIMO_TOOL_STATUS.Running }), MessageCompletion.UNSPECIFIED)).toBe('request')
  })
})

describe('mimoToolImages', () => {
  it('reads each picture in the order the call attached them', () => {
    const part = toolPart({
      attachments: [
        { type: 'file', mime: 'image/png', url: 'data:image/png;base64,iVBORw0KGgo=' },
        { type: 'file', mime: 'image/jpeg', url: 'data:image/jpeg;base64,/9j/4AAQ' },
      ],
    })
    expect(mimoToolImages(part)).toEqual([
      { mimeType: 'image/png', data: 'iVBORw0KGgo=' },
      { mimeType: 'image/jpeg', data: '/9j/4AAQ' },
    ])
  })

  // A PDF or a binary resource is a file the model received, not a picture the
  // row can draw.
  it('skips an attachment that is not a picture', () => {
    const part = toolPart({
      attachments: [
        { type: 'file', mime: 'application/pdf', url: 'data:application/pdf;base64,JVBERi0=' },
        { type: 'file', mime: 'image/png', url: 'https://example.com/a.png' },
        { type: 'file', mime: 'image/png' },
        { type: 'file', mime: 'image/png', url: 'data:image/png;base64,iVBORw0KGgo=' },
      ],
    })
    expect(mimoToolImages(part)).toEqual([{ mimeType: 'image/png', data: 'iVBORw0KGgo=' }])
  })

  it('reads no picture from a call that attached nothing', () => {
    expect(mimoToolImages(toolPart({}))).toEqual([])
  })
})
