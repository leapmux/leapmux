import { describe, expect, it } from 'vitest'
import { kimiTranscriptFrame } from '~/test-support/kimiFixtures'
import { kimiTranscriptRetry, kimiTranscriptRow, kimiTranscriptTurn } from './transcript'

describe('kimiTranscriptRow', () => {
  it('preserves the native operation and its ownership', () => {
    const operation = {
      op: 'frame.upsert',
      extra: { zero: 0, enabled: false },
    }
    const frame = kimiTranscriptFrame(operation, 'agent-1', 'session_other')
    const before = structuredClone(frame)
    const row = kimiTranscriptRow(frame)
    expect(row).toStrictEqual({ sessionId: 'session_other', agentId: 'agent-1', operation })
    expect(frame).toStrictEqual(before)
  })

  it('accepts zero and an absent sequence', () => {
    const frame = kimiTranscriptFrame({ op: 'turn.upsert' })
    for (const seq of [0, undefined]) {
      expect(kimiTranscriptRow({
        ...frame,
        payload: { ...frame.payload, seq },
      })).not.toBeNull()
    }
  })

  it('rejects malformed or ambiguous operations and ownership', () => {
    const frame = kimiTranscriptFrame({ op: 'turn.upsert' })
    const invalid = [
      null,
      {},
      { ...frame, type: 'transcript.reset' },
      { ...frame, session_id: '' },
      {
        ...frame,
        payload: { ...frame.payload, type: 'agent' },
      },
      {
        ...frame,
        payload: { ...frame.payload, agent_id: '' },
      },
      {
        ...frame,
        payload: { ...frame.payload, ops: [] },
      },
      {
        ...frame,
        payload: {
          ...frame.payload,
          ops: [null],
        },
      },
      {
        ...frame,
        payload: {
          ...frame.payload,
          ops: [frame.payload.ops[0], frame.payload.ops[0]],
        },
      },
      ...[
        -1,
        1.5,
        '1',
        null,
        Number.MAX_SAFE_INTEGER + 1,
      ].map(seq => ({
        ...frame,
        payload: { ...frame.payload, seq },
      })),
    ]
    for (const value of invalid)
      expect(kimiTranscriptRow(value)).toBeNull()
  })
})

describe('kimiTranscriptTurn', () => {
  it('requires a native turn header with a string identity', () => {
    const turn = { kind: 'turn', turnId: 't0', state: 'completed' }
    expect(kimiTranscriptTurn(kimiTranscriptFrame({ op: 'turn.upsert', turn }))).toStrictEqual(turn)
    for (const value of [
      null,
      {},
      { ...turn, turnId: 0 },
      { ...turn, turnId: '' },
      { ...turn, kind: 'step' },
    ])
      expect(kimiTranscriptTurn(kimiTranscriptFrame({ op: 'turn.upsert', turn: value }))).toBeNull()
    expect(kimiTranscriptTurn(kimiTranscriptFrame({ op: 'step.upsert', turn }))).toBeNull()
  })
})

describe('kimiTranscriptRetry', () => {
  it.each([
    {},
    { failedAttempt: 0 },
    { failedAttempt: -1 },
    { nextAttempt: 1 },
    { nextAttempt: 2.5 },
    { maxAttempts: 1 },
    { delayMs: -1 },
    { delayMs: Number.POSITIVE_INFINITY },
    { failedAttempt: Number.MAX_SAFE_INTEGER + 1 },
  ])('rejects incomplete or invalid retry facts %j', (override) => {
    const retry = Object.keys(override).length === 0
      ? {}
      : {
          failedAttempt: 1,
          nextAttempt: 2,
          maxAttempts: 10,
          delayMs: 0,
          ...override,
        }
    expect(kimiTranscriptRetry(kimiTranscriptFrame({
      op: 'step.upsert',
      turnId: 't0',
      step: {
        kind: 'step',
        stepId: 't0.1',
        state: 'running',
        retry,
      },
    }))).toBeNull()
  })
})
