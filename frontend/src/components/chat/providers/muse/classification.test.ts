import { describe, expect, it } from 'vitest'
import { input } from '~/components/chat/providers/testUtils'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerRow } from '~/test-support/toolCallFixture'
import { classifyMuseMessage } from './classification'
import '~/components/chat/providers'
import '~/components/chat/providers/testMocks'

describe('classifyMuseMessage', () => {
  it.each([
    {
      method: 'item/completed',
      params: {
        sessionId: 'child',
        item: {
          itemId: 'text',
          revision: 1,
          kind: 'agentMessage',
          status: 'completed',
          text: 'Unadmitted text',
        },
      },
    },
    {
      method: 'item/completed',
      params: {
        sessionId: 'child',
        item: {
          itemId: 'tool',
          revision: 1,
          kind: 'toolCall',
          status: 'completed',
          tool: 'bash',
          args: '{}',
        },
      },
    },
    {
      method: 'turn/completed',
      params: { sessionId: 'child', turnId: 'turn', terminal: 'completed' },
    },
    {
      method: 'future/event',
      params: { sessionId: 'child', viewCursor: 'cursor' },
    },
  ])('retains refused projection evidence without rendering its native $method frame', (payload) => {
    const original = structuredClone(payload)
    expect(providerRow(AgentProvider.MUSE_CODE, payload, {
      supplementalContent: {
        nativeViewProjectionUnavailable: { sessionId: 'child', reason: 'The Muse view could not be projected.' },
      },
    })).toEqual({ kind: 'hidden' })
    expect(payload).toEqual(original)
  })

  it.each([
    undefined,
    null,
    {},
    { sessionId: '', reason: 'Unavailable' },
    { sessionId: 'child' },
    { sessionId: 'foreign', reason: 'Unavailable' },
  ])('rejects an invalid projection-failure marker %j', (marker) => {
    const payload = {
      method: 'item/completed',
      params: {
        sessionId: 'child',
        item: {
          itemId: 'text',
          revision: 1,
          kind: 'agentMessage',
          status: 'completed',
          text: 'Native text',
        },
      },
    }
    expect(providerRow(AgentProvider.MUSE_CODE, payload, {
      supplementalContent: { nativeViewProjectionUnavailable: marker },
    }))
      .toEqual({ kind: 'assistant-text', text: 'Native text' })
  })

  it.each([
    'pending',
    'inProgress',
    'completed',
    'cancelled',
  ])('hides the recognized native %s list update from chat', (status) => {
    const frame = {
      method: 'session/todoListChanged',
      params: {
        sessionId: 'native-session',
        items: [
          { text: 'Native task', status },
        ],
      },
    }
    const original = structuredClone(frame)
    expect(providerRow(AgentProvider.MUSE_CODE, frame)).toEqual({ kind: 'hidden' })
    expect(frame).toEqual(original)
  })

  it('hides an actual native clear while the sidebar owns the list', () => {
    expect(providerRow(AgentProvider.MUSE_CODE, {
      method: 'session/todoListChanged',
      params: { items: [] },
    })).toEqual({ kind: 'hidden' })
  })

  it('shows an unknown native list status through the neutral notification model', () => {
    const frame = {
      method: 'session/todoListChanged',
      params: {
        items: [
          { text: 'Native task', status: 'futureStatus' },
        ],
      },
    }
    expect(providerRow(AgentProvider.MUSE_CODE, frame)).toEqual({
      kind: 'notification',
      thread: {
        entries: [
          { kind: 'text', text: 'Unknown Muse to-do status: futureStatus' },
        ],
      },
    })
  })

  it('keeps consolidated recognized native list updates hidden', () => {
    const frame = {
      type: 'notification_thread',
      messages: [
        {
          method: 'session/todoListChanged',
          params: {
            items: [
              { text: 'Native task', status: 'completed' },
            ],
          },
        },
        {
          method: 'session/todoListChanged',
          params: { items: [] },
        },
      ],
    }
    const original = structuredClone(frame)
    expect(classifyMuseMessage(input(undefined, { old_seqs: [], messages: frame.messages }, AgentProvider.MUSE_CODE))).toEqual({ kind: 'hidden' })
    expect(frame).toEqual(original)
  })
})

describe('classifyMuseMessage native outcomes', () => {
  it.each([
    ['answered', 'Muse question answered.'],
    ['cancelled', 'Muse question cancelled.'],
    ['interrupted', 'Muse question interrupted.'],
    ['clarified', 'Muse question clarified.'],
    ['timedOut', 'Muse question timed out.'],
    ['aborted', 'Muse question aborted.'],
    ['futureOutcome', 'Muse question settled: futureOutcome'],
  ])('classifies native question outcome %s through the neutral notification model', (outcome, text) => {
    const frame = {
      method: 'userInput/settled',
      params: { sessionId: 'native-session', userInputId: 'native-input', outcome },
    }
    const original = structuredClone(frame)
    expect(providerRow(AgentProvider.MUSE_CODE, frame)).toEqual({
      kind: 'notification',
      thread: {
        entries: [
          { kind: 'text', text },
        ],
      },
    })
    expect(frame).toEqual(original)
  })

  it.each([
    ['noop', 'skipped'],
    ['failed', 'failed'],
    ['cancelled', 'aborted'],
  ])('classifies native compaction outcome %s through the neutral notification model', (outcome, text) => {
    const frame = {
      method: 'item/completed',
      params: {
        sessionId: 'native-session',
        item: { itemId: 'compaction', kind: 'compaction', outcome },
      },
    }
    const original = structuredClone(frame)
    expect(providerRow(AgentProvider.MUSE_CODE, frame)).toEqual({
      kind: 'notification',
      thread: {
        entries: [
          { kind: 'compaction', phase: 'end', outcome: text },
        ],
      },
    })
    expect(frame).toEqual(original)
  })
})

describe('classifyMuseMessage hooks', () => {
  it('renders a failed hook through the neutral model', () => {
    const frame = {
      method: 'item/completed',
      params: {
        sessionId: 'native-session',
        item: {
          itemId: 'hook',
          kind: 'hookRun',
          status: 'failed',
          event: 'Stop',
          reason: 'The hook failed.',
        },
      },
    }
    expect(providerRow(AgentProvider.MUSE_CODE, frame)).toEqual({
      kind: 'notification',
      thread: {
        entries: [
          {
            kind: 'hook',
            outcome: 'failed',
            event: 'Stop',
            detail: 'The hook failed.',
          },
        ],
      },
    })
  })

  it('keeps an unknown hook outcome visible', () => {
    const frame = {
      method: 'item/completed',
      params: {
        sessionId: 'native-session',
        item: { itemId: 'hook', kind: 'hookRun', status: 'futureStatus' },
      },
    }
    expect(classifyMuseMessage(input(frame, undefined, AgentProvider.MUSE_CODE))).toMatchObject({ kind: 'notification' })
  })
})

describe('classifyMuseMessage obsolete hidden rows', () => {
  it.each(['subagent', 'reminderChild'])('classifies a stored %s item as unknown', (kind) => {
    const frame = {
      method: 'item/completed',
      params: {
        sessionId: 'native-session',
        item: { itemId: 'item', kind, status: 'completed' },
      },
    }
    expect(classifyMuseMessage(input(frame, undefined, AgentProvider.MUSE_CODE))).toEqual({ kind: 'unknown' })
  })

  it('classifies a non-final workflow row as unknown', () => {
    const frame = {
      method: 'item/started',
      params: {
        sessionId: 'native-session',
        item: { itemId: 'item', kind: 'workflow', status: 'inProgress' },
      },
    }
    expect(classifyMuseMessage(input(frame, undefined, AgentProvider.MUSE_CODE))).toEqual({ kind: 'unknown' })
  })

  it('keeps the finished workflow result', () => {
    const frame = {
      method: 'item/completed',
      params: {
        sessionId: 'native-session',
        item: { itemId: 'item', kind: 'workflow', status: 'completed' },
      },
    }
    expect(classifyMuseMessage(input(frame, undefined, AgentProvider.MUSE_CODE))).toEqual({ kind: 'tool_result' })
  })
})
