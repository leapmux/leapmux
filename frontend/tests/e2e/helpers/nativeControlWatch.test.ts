import type { MessageInitShape } from '@bufbuild/protobuf'
import type { ControlPrompt } from '../../../src/components/chat/model/controlPrompt'
import type { ControlPromptSchema } from '../../../src/generated/proto/leapmux/v1/agent_control_pb'
import type { NativeControlFrame } from './nativeControlWatch'
import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { PermissionOptionKind } from '../../../src/generated/proto/leapmux/v1/agent_control_pb'
import { ControlResponseState } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { AgentEventSchema } from '../../../src/generated/proto/leapmux/v1/watch_pb'
import { nativeControlFrame, readsProviderPermission } from './nativeControlWatch'

/** The prompt that the Worker reads from a native permission for a shell command. */
const WIRE_PROMPT: MessageInitShape<typeof ControlPromptSchema> = {
  kind: {
    case: 'permission',
    value: {
      title: 'Bash',
      options: [
        { optionId: 'allow', kind: PermissionOptionKind.ALLOW_ONCE, name: 'Allow' },
      ],
    },
  },
}

/** The same prompt in the model that the banner draws. */
const MODEL_PROMPT: ControlPrompt = {
  kind: 'permission',
  permission: {
    title: 'Bash',
    options: [
      { optionId: 'allow', kind: 'allow_once', name: 'Allow' },
    ],
  },
}

function controlEvent(payload: string, options: { changed?: boolean, requestId?: string } = {}) {
  return create(AgentEventSchema, {
    agentId: 'agent-1',
    event: {
      case: options.changed ? 'controlResponseChanged' : 'controlRequest',
      value: {
        requestId: options.requestId ?? 'native-control-1',
        payload: new TextEncoder().encode(payload),
        responseState: options.changed ? ControlResponseState.COMPLETED : ControlResponseState.READY,
        prompt: WIRE_PROMPT,
      },
    },
  })
}

/** The error that the selector throws for one event, so a test can read its cause. */
function selectorFailure(event: ReturnType<typeof controlEvent>): Error {
  try {
    nativeControlFrame(event)
  }
  catch (error) {
    return error as Error
  }
  throw new Error('The selector accepted the event.')
}

describe('nativeControlFrame', () => {
  it('captures a real control request, its prompt, and its later completed response state', () => {
    expect(nativeControlFrame(controlEvent('{"type":"native-permission","enabled":false,"count":0,"input":""}'))).toEqual({
      requestId: 'native-control-1',
      payload: {
        type: 'native-permission',
        enabled: false,
        count: 0,
        input: '',
      },
      prompt: MODEL_PROMPT,
      responseState: ControlResponseState.READY,
    })
    expect(nativeControlFrame(controlEvent('{"type":"native-permission"}', { changed: true }))).toEqual({
      requestId: 'native-control-1',
      payload: { type: 'native-permission' },
      prompt: MODEL_PROMPT,
      responseState: ControlResponseState.COMPLETED,
    })
  })

  it('captures a completed cancellation after the native request withdraws', () => {
    expect(nativeControlFrame(create(AgentEventSchema, {
      agentId: 'agent-1',
      event: {
        case: 'controlCancel',
        value: {
          requestId: 'native-control-1',
          claimToken: 'claim-1',
          responseState: ControlResponseState.COMPLETED,
        },
      },
    }))).toEqual({
      requestId: 'native-control-1',
      claimToken: 'claim-1',
      payload: {},
      canceled: true,
      responseState: ControlResponseState.COMPLETED,
    })
  })

  it('skips a response-state update that contains no native payload, but refuses an empty request payload', () => {
    expect(nativeControlFrame(controlEvent('', { changed: true }))).toBeUndefined()
    expect(selectorFailure(controlEvent('')).message).toBe('The Worker sent an invalid native control frame.')
  })

  it('skips an event that is not a control', () => {
    expect(nativeControlFrame(create(AgentEventSchema, {
      agentId: 'agent-1',
      event: { case: 'inputQueueChanged', value: {} },
    }))).toBeUndefined()
  })

  it('refuses a control with no request ID', () => {
    expect(selectorFailure(controlEvent('{}', { requestId: '' })).cause).toMatchObject({ message: 'The native control frame has no request ID.' })
  })

  it.each([
    '{invalid',
    'null',
    '[]',
    '"text"',
    '0',
  ])('refuses malformed or non-object native control content: %s', (payload) => {
    const failure = selectorFailure(controlEvent(payload))
    expect(failure.message).toBe('The Worker sent an invalid native control frame.')
    expect(failure.cause).toBeInstanceOf(Error)
  })
})

describe('readsProviderPermission', () => {
  /** A frame whose Worker prompt is `prompt`. */
  function frame(prompt?: ControlPrompt): NativeControlFrame {
    return {
      requestId: 'native-control-1',
      payload: {},
      responseState: ControlResponseState.READY,
      ...(prompt
        ? { prompt }
        : {}),
    }
  }

  it.each<ControlPrompt>([
    MODEL_PROMPT,
    {
      kind: 'permission',
      permission: { reason: 'The agent asks to write', options: [] },
    },
    {
      kind: 'permission',
      permission: { text: 'Write the file', options: [] },
    },
    {
      kind: 'permission',
      permission: { command: 'ls', options: [] },
    },
    {
      kind: 'permission',
      permission: { input: {}, options: [] },
    },
    {
      kind: 'permission',
      permission: { input: null, options: [] },
    },
  ])('counts a permission that states what it asks about: %j', (prompt) => {
    expect(readsProviderPermission(frame(prompt))).toBe(true)
  })

  it('does not count the generic prompt of a payload that the provider does not recognize', () => {
    expect(readsProviderPermission(frame({
      kind: 'permission',
      permission: {
        options: [
          { optionId: 'leapmux:allow', kind: 'allow_once' },
          { optionId: 'leapmux:deny', kind: 'reject_once' },
        ],
      },
    }))).toBe(false)
  })

  it.each<[string, ControlPrompt | undefined]>([
    [
      'a question',
      {
        kind: 'question',
        questions: [
          { question: 'Which?', options: [] },
        ],
      },
    ],
    [
      'a plan',
      { kind: 'plan', text: '1. Do it' },
    ],
    ['a cancellation', undefined],
  ])('does not count %s', (_label, prompt) => {
    expect(readsProviderPermission(frame(prompt))).toBe(false)
  })
})
