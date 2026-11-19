import { describe, expect, it } from 'vitest'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerToolCall } from '~/test-support/toolCallFixture'
import { resolveMessageForRendering } from '../registry'
import { classifyKimiMessage } from './classification'
import { kimiConversationRow } from './conversation'
import { kimiNotificationEntry } from './extractors/notification'
import { kimiResultDivider } from './extractors/resultDivider'
import { kimiExtractRow } from './extractors/row'
import { resolveKimiMessage } from './resolveMessage'
import { kimiSpanRole } from './spanRole'
import '../index'

const timestamp = '2026-11-18T00:00:00Z'

describe('native conversation fields', () => {
  const user = {
    type: 'user_message',
    text: 'native prompt',
    prompt_ids: ['prompt-1'],
    placement: 'opener',
    origin: { kind: 'user' },
    attachments: [],
    skills: [],
  }

  it.each([
    {
      origin: { kind: 'unknown' },
    },
    {
      origin: { kind: 'skill' },
    },
    {
      origin: { kind: 'plugin_command', plugin_id: 'plugin' },
    },
    {
      origin: { kind: 'cron', job_id: 'job' },
    },
    {
      origin: { kind: 'hook' },
    },
    {
      attachments: [
        { kind: 'image', ordinal: 0 },
      ],
    },
    {
      attachments: [
        { kind: 'image', ordinal: 1, size: -1 },
      ],
    },
    {
      skills: [{}],
    },
    { metadata: [] },
  ])('refuses malformed native user metadata: %j', (patch) => {
    expect(kimiConversationRow(itemRecord({
      ...user,
      ...patch,
    }))).toBeNull()
  })

  it.each([
    { kind: 'user' },
    { kind: 'goal' },
    { kind: 'retry' },
    { kind: 'system' },
    { kind: 'skill', name: 'native skill' },
    { kind: 'plugin_command', plugin_id: 'plugin', command: 'command' },
    {
      kind: 'cron',
      job_id: 'job',
      schedule: '* * * * *',
      recurring: false,
      coalesced_count: 0,
      stale: false,
    },
    { kind: 'hook', hook_event: 'UserPromptSubmit' },
  ])('retains a valid native user origin: %j', (origin) => {
    const row = kimiConversationRow(itemRecord({ ...user, origin }))
    expect(row?.kind).toBe('item')
    if (row?.kind === 'item')
      expect(row.item.origin).toEqual(origin)
  })
})

describe('conversation subscription failures', () => {
  const failure = {
    type: 'leapmux.kimi.failure',
    session_id: 'native',
    agent_id: 'main',
    event_created_at: timestamp,
    failure: {
      type: 'turns.failed',
      session_id: 'native',
      agent_id: 'main',
      event_created_at: timestamp,
      code: 0,
      msg: 'Native conversation storage unavailable',
    },
  }

  it('draws a conversation subscription failure without inventing a turn outcome', () => {
    expect(kimiConversationRow(failure)?.kind).toBe('failure')
    expect(kimiNotificationEntry(failure)).toEqual([
      { kind: 'text', text: 'Conversation history could not be loaded: Native conversation storage unavailable (code 0)' },
    ])
    expect(kimiResultDivider(failure)).toBeNull()
  })

  it('refuses a failure packet attached to a retry record', () => {
    expect(kimiConversationRow({
      type: 'leapmux.kimi.retry',
      session_id: 'native',
      agent_id: 'main',
      turn_id: 'turn-1',
      step_index: 1,
      retry: {
        attempt: 1,
        next_attempt: 2,
        max_attempts: 3,
        delay_ms: 0,
        error: { name: 'Error', message: 'retry' },
      },
      failure: failure.failure,
    })).toBeNull()
  })

  it.each([
    { session_id: 'foreign' },
    { agent_id: 'foreign' },
    { type: 'turn.ended' },
    { event_created_at: '2026-11-18T00:00:01Z' },
  ])('refuses a failure packet that changes its native owner: %j', (patch) => {
    expect(kimiConversationRow({
      ...failure,
      failure: {
        ...failure.failure,
        ...patch,
      },
    })).toBeNull()
  })
})

function itemRecord(item: Record<string, unknown>, role = 'item') {
  return {
    type: 'leapmux.kimi.item',
    session_id: 'native',
    agent_id: 'main',
    turn_id: 'turn-1',
    role,
    item: {
      item_id: 'item:1',
      status: 'ended',
      outcome: 'completed',
      created_at: timestamp,
      ...item,
    },
  }
}

function parsed(record: Record<string, unknown>, supplementalContent?: Record<string, unknown>, agentSessionId = 'native') {
  return {
    rawText: '',
    topLevel: record,
    parentObject: record,
    wrapper: null,
    agentSessionId,
    supplementalContent,
  }
}

describe('classifyKimiMessage', () => {
  it('keeps native system completion out of model result dividers', () => {
    const record = {
      type: 'leapmux.kimi.turn',
      session_id: 'native',
      agent_id: 'main',
      turn_id: 'system-turn',
      turn: {
        turn_id: 'system-turn',
        ordinal: 0,
        kind: 'system',
        status: 'ended',
        outcome: 'completed',
        started_at: timestamp,
        steps: [],
      },
    }
    const resolved = resolveMessageForRendering(parsed(record), AgentProvider.KIMI_CODE)
    expect(classifyKimiMessage({ ...resolved, agentProvider: AgentProvider.KIMI_CODE })).toStrictEqual({ kind: 'hidden' })
    expect(kimiResultDivider(record)).toBeNull()
  })

  it('reads a native hook result only with its explicit blocked turn context', () => {
    const record = itemRecord({ type: 'assistant_message', text: '<hook_result hook_event="UserPromptSubmit">\nNative rejection reason\n</hook_result>' })
    const blocked = {
      ...record,
      turn_context: {
        turn_id: 'turn-1',
        kind: 'agent',
        status: 'ended',
        outcome: 'failed',
        end_reason: 'blocked',
      },
    }
    const resolved = resolveMessageForRendering(parsed(blocked), AgentProvider.KIMI_CODE)
    expect(classifyKimiMessage({ ...resolved, agentProvider: AgentProvider.KIMI_CODE })).toStrictEqual({
      kind: 'notification',
      entries: [
        { kind: 'text', text: 'Hook UserPromptSubmit blocked: Native rejection reason' },
      ],
    })
    expect(kimiNotificationEntry(record)).toStrictEqual([])
    const ordinary = resolveMessageForRendering(parsed(record), AgentProvider.KIMI_CODE)
    expect(classifyKimiMessage({ ...ordinary, agentProvider: AgentProvider.KIMI_CODE })).toStrictEqual({ kind: 'assistant_text' })
  })

  it('keeps a native hook refusal from becoming a model result divider', () => {
    const record = {
      type: 'leapmux.kimi.turn',
      session_id: 'native',
      agent_id: 'main',
      turn_id: 'turn-1',
      turn: {
        turn_id: 'turn-1',
        ordinal: 0,
        kind: 'agent',
        status: 'ended',
        outcome: 'failed',
        end_reason: 'blocked',
        started_at: timestamp,
        steps: [],
      },
    }
    const resolved = resolveMessageForRendering(parsed(record), AgentProvider.KIMI_CODE)
    expect(classifyKimiMessage({ ...resolved, agentProvider: AgentProvider.KIMI_CODE })).toStrictEqual({ kind: 'hidden' })
    expect(kimiResultDivider(record)).toBeNull()
  })

  it('reads a native child prompt while removing only its leading git context', () => {
    const record = itemRecord({
      type: 'user_message',
      text: '<git-context>branch main</git-context>\nRead the child file.',
      prompt_ids: [],
      placement: 'opener',
      origin: { kind: 'system' },
      attachments: [],
      skills: [],
    })
    record.agent_id = 'agent-0'
    const resolved = resolveMessageForRendering(parsed(record), AgentProvider.KIMI_CODE)
    const category = classifyKimiMessage({ ...resolved, agentProvider: AgentProvider.KIMI_CODE })
    expect(category).toStrictEqual({ kind: 'user_text' })
    expect(kimiExtractRow({
      resolved,
      category,
      span: {
        request: undefined,
        role: 'other',
        visibleRows: { request: false },
      },
    })).toStrictEqual({ kind: 'user', text: 'Read the child file.', attachments: [] })
  })

  it.each([
    ['assistant_message', 'assistant_text', 'assistant-text'],
    ['reasoning', 'assistant_thinking', 'assistant-thinking'],
  ])('reads native %s items through the public hooks', (type, category, rowKind) => {
    const record = itemRecord({ type, text: 'Native answer 🙂', started_at: timestamp })
    const resolved = resolveMessageForRendering(parsed(record), AgentProvider.KIMI_CODE)
    const classified = classifyKimiMessage({ ...resolved, agentProvider: AgentProvider.KIMI_CODE })
    expect(classified).toStrictEqual({ kind: category })
    expect(kimiExtractRow({
      resolved,
      category: classified,
      span: {
        request: undefined,
        role: 'other',
        visibleRows: { request: false },
      },
    })).toStrictEqual({ kind: rowKind, text: 'Native answer 🙂' })
  })

  it('uses the immutable role for a recovered request whose native item already ended', () => {
    const record = itemRecord({
      type: 'tool_call',
      name: 'Bash',
      input: { command: 'printf once' },
      started_at: timestamp,
    }, 'request')
    const resolved = resolveMessageForRendering(parsed(record), AgentProvider.KIMI_CODE)
    expect(classifyKimiMessage({ ...resolved, agentProvider: AgentProvider.KIMI_CODE })).toStrictEqual({ kind: 'tool_use' })
    expect(kimiSpanRole(resolved)).toBe('request')
    const attemptedResult = {
      ...record,
      item: { ...record.item, output: 'unseen output' },
    }
    expect(resolveKimiMessage(parsed(record, { kimi_conversation: attemptedResult }))).toBeUndefined()
  })
})

describe('resolveKimiMessage', () => {
  it('reports truncation from the retained public result for an interrupted command', () => {
    const kept = itemRecord({
      type: 'tool_call',
      name: 'Bash',
      input: { command: 'printf output' },
      started_at: timestamp,
      status: 'running',
      output: 'Latest output🙂',
    }, 'result')
    const record = { ...kept, output_truncated: true }
    const call = providerToolCall(AgentProvider.KIMI_CODE, record, { completion: MessageCompletion.INTERRUPTED })
    expect(call?.status).toBe('cancelled')
    expect(call?.truncated).toBe(true)
    expect(call?.result).toMatchObject({
      commands: [
        { output: 'Latest output🙂' },
      ],
    })
    const complete = {
      ...kept,
      item: {
        ...kept.item,
        status: 'ended',
        outcome: 'completed',
        output: 'Full output',
      },
    }
    expect(providerToolCall(AgentProvider.KIMI_CODE, complete)?.truncated).toBeUndefined()
  })

  it('refuses an output truncation flag on a request or a record with no output', () => {
    const item = {
      type: 'tool_call',
      name: 'Bash',
      started_at: timestamp,
      input: { command: 'printf output' },
    }
    for (const [role, fields] of [
      [
        'request',
        { ...item, output: 'present' },
      ],
      ['result', item],
    ] as const) {
      expect(kimiConversationRow({
        ...itemRecord(fields, role),
        output_truncated: true,
      })).toBeNull()
    }
    expect(kimiConversationRow({
      ...itemRecord({ ...item, output: 'present' }, 'result'),
      output_truncated: 'true',
    })).toBeNull()
  })

  it('refuses a correction that removes accepted turn context', () => {
    const original = {
      ...itemRecord({ type: 'assistant_message', text: 'Original' }),
      turn_context: { turn_id: 'turn-1', kind: 'agent', status: 'running' },
    }
    const latest = itemRecord({ type: 'assistant_message', text: 'Latest' })
    expect(resolveKimiMessage(parsed(original, { kimi_conversation: latest }))).toBeUndefined()
  })
  it('resolves verified file bytes only for the native result that requested them', () => {
    const fileID = `f_${'a'.repeat(64)}`
    const record = itemRecord({
      type: 'tool_call',
      name: 'ReadMediaFile',
      started_at: timestamp,
      input: { path: 'image.png' },
      output: 'Loaded image.png',
      media: [
        { kind: 'image', file_id: fileID },
      ],
    }, 'result')
    const before = structuredClone(record)
    const receipt = {
      session_id: 'native',
      agent_id: 'main',
      turn_id: 'turn-1',
      item_id: 'item:1',
      files: {
        [fileID]: { mimeType: 'image/png', data: 'aW1hZ2U=' },
      },
    }
    expect(providerToolCall(AgentProvider.KIMI_CODE, record, {
      agentSessionId: 'native',
      supplementalContent: { kimi_media: receipt },
    })?.images).toStrictEqual([
      { mimeType: 'image/png', data: 'aW1hZ2U=' },
    ])
    expect(record).toStrictEqual(before)
    for (const field of [
      'session_id',
      'agent_id',
      'turn_id',
      'item_id',
    ]) {
      const foreign = { ...receipt, [field]: 'foreign' }
      expect(providerToolCall(AgentProvider.KIMI_CODE, record, {
        agentSessionId: 'native',
        supplementalContent: { kimi_media: foreign },
      })?.images).toStrictEqual([])
    }
    const latest = {
      ...record,
      item: {
        ...record.item,
        media: [
          { kind: 'image', file_id: `f_${'b'.repeat(64)}` },
        ],
      },
    }
    expect(providerToolCall(AgentProvider.KIMI_CODE, record, {
      agentSessionId: 'native',
      supplementalContent: { kimi_conversation: latest, kimi_media: receipt },
    })?.images).toStrictEqual([])
  })

  it('resolves an authoritative text correction without changing the original', () => {
    const original = itemRecord({ type: 'assistant_message', text: 'Prefix', status: 'running' })
    const latest = itemRecord({ type: 'assistant_message', text: 'Final answer' })
    const before = structuredClone(original)
    expect(resolveKimiMessage(parsed(original, { kimi_conversation: latest }))).toStrictEqual(latest)
    expect(original).toStrictEqual(before)
  })

  it.each([
    'session_id',
    'agent_id',
    'turn_id',
    'role',
    'item_id',
    'type',
    'step_index',
  ])('rejects another %s in a rendering correction', (field) => {
    const original = itemRecord({ type: 'assistant_message', text: 'Original', step_index: 1 })
    const latest = itemRecord({ type: 'assistant_message', text: 'Foreign', step_index: 1 })
    if (field === 'item_id' || field === 'type' || field === 'step_index') {
      Object.assign(latest.item, {
        [field]: field === 'step_index'
          ? 2
          : 'foreign',
      })
    }
    else {
      Object.assign(latest, { [field]: 'foreign' })
    }
    expect(resolveKimiMessage(parsed(original, { kimi_conversation: latest }))).toBeUndefined()
  })

  it.each(['', 'foreign'])('refuses a rendering correction for stored session %s', (session) => {
    const original = itemRecord({ type: 'assistant_message', text: 'Original' })
    const latest = itemRecord({ type: 'assistant_message', text: 'Latest' })
    expect(resolveKimiMessage(parsed(original, { kimi_conversation: latest }, session))).toBeUndefined()
  })
})

describe('kimiConversationRow', () => {
  it('requires matching nonempty string identities on a public turn', () => {
    const turn = {
      turn_id: 'turn-1',
      ordinal: 0,
      kind: 'agent',
      status: 'ended',
      outcome: 'completed',
      started_at: timestamp,
      steps: [],
    }
    const record = {
      type: 'leapmux.kimi.turn',
      session_id: 'native',
      agent_id: 'main',
      turn_id: 'turn-1',
      turn,
    }
    expect(kimiConversationRow(record)).toMatchObject({ kind: 'turn', turn })
    for (const turn_id of [null, '', 0, 'foreign']) {
      expect(kimiConversationRow({
        ...record,
        turn: { ...turn, turn_id },
      })).toBeNull()
    }
  })

  it('refuses item context on turn and notice records', () => {
    const owner = { session_id: 'native', agent_id: 'main' }
    const turn = {
      turn_id: 'turn-1',
      ordinal: 0,
      kind: 'agent',
      status: 'ended',
      outcome: 'completed',
      started_at: timestamp,
      steps: [],
    }
    const context = { turn_id: 'turn-1', kind: 'agent', status: 'ended' }
    expect(kimiConversationRow({
      ...owner,
      type: 'leapmux.kimi.turn',
      turn_id: 'turn-1',
      turn,
      turn_context: context,
    })).toBeNull()
    expect(kimiConversationRow({
      ...owner,
      type: 'leapmux.kimi.notice',
      event_created_at: timestamp,
      notice: { level: 'info', title: 'Native notice' },
      turn_context: context,
    })).toBeNull()
  })

  it.each([
    null,
    0,
    [],
    {},
    { turn_id: 'foreign', kind: 'agent', status: 'ended' },
    { turn_id: 'turn-1', kind: 'foreign', status: 'ended' },
    { turn_id: 'turn-1', kind: 'agent', status: 'foreign' },
    {
      turn_id: 'turn-1',
      kind: 'agent',
      status: 'ended',
      outcome: 'foreign',
    },
    {
      turn_id: 'turn-1',
      kind: 'agent',
      status: 'ended',
      end_reason: 0,
    },
  ].map(context => ({ context })))('rejects invalid turn context %j', ({ context }) => {
    expect(kimiConversationRow({
      ...itemRecord({ type: 'assistant_message', text: 'Text' }),
      turn_context: context,
    })).toBeNull()
  })

  it('refuses outcome context on a native tool request', () => {
    expect(kimiConversationRow({
      ...itemRecord({
        type: 'tool_call',
        name: 'Bash',
        started_at: timestamp,
        input: {},
      }, 'request'),
      turn_context: { turn_id: 'turn-1', kind: 'agent', status: 'ended' },
    })).toBeNull()
  })
})

describe('kimiToolRow', () => {
  it('reads native image media separately from textual tool output', () => {
    const data = 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aY9sAAAAASUVORK5CYII='
    const record = itemRecord({
      type: 'tool_call',
      name: 'ReadMediaFile',
      started_at: timestamp,
      input: { path: 'image.png' },
      output: 'Loaded image.png',
      media: [
        { kind: 'image', url: `data:image/png;base64,${data}`, media_type: 'image/png' },
      ],
    }, 'result')
    const call = providerToolCall(AgentProvider.KIMI_CODE, record, { agentSessionId: 'native' })
    expect(call?.images).toStrictEqual([
      { mimeType: 'image/png', data },
    ])
  })

  it('keeps media off a request and refuses unresolved or foreign image URLs', () => {
    const record = itemRecord({
      type: 'tool_call',
      name: 'ReadMediaFile',
      started_at: timestamp,
      input: { path: 'image.png' },
      media: [
        null,
        { kind: 'image', url: 'https://example.com/image.png' },
        { kind: 'image', url: 'blobref:unresolved' },
        { kind: 'image', url: 'data:text/html;base64,aW1hZ2U=' },
        { kind: 'image', url: 'data:image/png;base64,%%%%' },
        { kind: 'image', url: 'data:image/png;base64,aW1hZ2V=' },
        { kind: 'audio', url: 'data:image/png;base64,aW1hZ2U=' },
      ],
    }, 'result')
    expect(providerToolCall(AgentProvider.KIMI_CODE, record, { agentSessionId: 'native' })?.images).toStrictEqual([])
    record.role = 'request'
    Object.assign(record.item, {
      media: [
        { kind: 'image', url: 'data:image/png;base64,aW1hZ2U=' },
      ],
    })
    expect(providerToolCall(AgentProvider.KIMI_CODE, record, { agentSessionId: 'native' })?.images).toStrictEqual([])
  })

  it('reads failed command output from the native error field', () => {
    const record = itemRecord({
      type: 'tool_call',
      name: 'Bash',
      started_at: timestamp,
      input: { command: 'printf actual-output; exit 7' },
      outcome: 'failed',
      error: '<system>ERROR: Tool execution failed.</system>\nactual-output\nCommand failed with exit code: 7.',
    }, 'result')
    const call = providerToolCall(AgentProvider.KIMI_CODE, record, { agentSessionId: 'native' })
    expect(call?.result).toMatchObject({
      commands: [
        {
          output: expect.stringContaining('actual-output'),
          exitCode: 7,
        },
      ],
    })
  })

  it('keeps an interrupted native result cancelled even without a recognizable error trailer', () => {
    const record = itemRecord({
      type: 'tool_call',
      name: 'Bash',
      started_at: timestamp,
      input: { command: 'run the held probe' },
      outcome: 'interrupted',
      error: 'Native retained partial output',
    }, 'result')
    const call = providerToolCall(AgentProvider.KIMI_CODE, record, { agentSessionId: 'native' })
    expect(call?.status).toBe('cancelled')
    expect(call?.result).toStrictEqual({
      commands: [
        { output: 'Native retained partial output' },
      ],
      unresolvedTerminals: [],
    })
  })
})

describe('kimiConversationRow retry records', () => {
  const retry = {
    type: 'leapmux.kimi.retry',
    session_id: 'native',
    agent_id: 'main',
    turn_id: 'turn-1',
    step_index: 1,
    retry: {
      attempt: 1,
      next_attempt: 2,
      max_attempts: 10,
      delay_ms: 0,
      error: { name: 'status', message: 'Failed' },
    },
  }

  it('classifies a native retry as a notification and preserves zero delay', () => {
    const resolved = resolveMessageForRendering(parsed(retry), AgentProvider.KIMI_CODE)
    expect(classifyKimiMessage({ ...resolved, agentProvider: AgentProvider.KIMI_CODE })).toStrictEqual({
      kind: 'notification',
      entries: [
        {
          kind: 'retry',
          scope: 'api',
          attempt: 2,
          maxAttempts: 10,
          delayMs: 0,
          error: 'Failed',
        },
      ],
    })
  })

  it.each([
    { session_id: '' },
    { agent_id: '' },
    { turn_id: '' },
    { step_index: 0 },
    { step_index: -1 },
    { step_index: 0.5 },
    { step_index: Number.MAX_SAFE_INTEGER + 1 },
    { role: 'item' },
    { item: {} },
    { turn: {} },
    { notice: {} },
    { turn_context: {} },
    { retry: null },
    {
      retry: { ...retry.retry, attempt: 0 },
    },
    {
      retry: { ...retry.retry, attempt: -1 },
    },
    {
      retry: { ...retry.retry, attempt: Number.MAX_SAFE_INTEGER + 1 },
    },
    {
      retry: { ...retry.retry, next_attempt: 1 },
    },
    {
      retry: { ...retry.retry, next_attempt: 2.5 },
    },
    {
      retry: { ...retry.retry, max_attempts: 1 },
    },
    {
      retry: { ...retry.retry, next_attempt: 0 },
    },
    {
      retry: { ...retry.retry, max_attempts: 0 },
    },
    {
      retry: { ...retry.retry, delay_ms: undefined },
    },
    {
      retry: { ...retry.retry, delay_ms: -1 },
    },
    {
      retry: { ...retry.retry, delay_ms: Number.POSITIVE_INFINITY },
    },
    {
      retry: {
        ...retry.retry,
        error: { name: 'status' },
      },
    },
  ])('refuses incomplete or mixed retry records: %j', (patch) => {
    const record = {
      ...retry,
      ...patch,
    }
    expect(kimiConversationRow(record)).toBeNull()
    expect(kimiNotificationEntry(record)).toStrictEqual([])
  })
})
