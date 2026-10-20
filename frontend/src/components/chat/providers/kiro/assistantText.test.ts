import { describe, expect, it } from 'vitest'
import { kiroAssistantTextSegments } from './assistantText'

describe('kiroAssistantTextSegments', () => {
  it('splits a reply around its steering note', () => {
    expect(kiroAssistantTextSegments('Done. [STEERING steer-09dcef28-892c-4f90-a171-c85b53e07736: probe handled] The rest.')).toEqual([
      { kind: 'text', text: 'Done. ' },
      { kind: 'steer-note', steerId: 'steer-09dcef28-892c-4f90-a171-c85b53e07736', note: 'probe handled' },
      { kind: 'text', text: ' The rest.' },
    ])
  })

  it('splits a reply with several notes', () => {
    expect(kiroAssistantTextSegments('[STEERING steer-a1: first] middle [STEERING steer-b2: second]')).toEqual([
      { kind: 'steer-note', steerId: 'steer-a1', note: 'first' },
      { kind: 'text', text: ' middle ' },
      { kind: 'steer-note', steerId: 'steer-b2', note: 'second' },
    ])
  })

  it('drops an empty note segment', () => {
    expect(kiroAssistantTextSegments('[STEERING steer-a1: ]')).toEqual([
      { kind: 'steer-note', steerId: 'steer-a1', note: '' },
    ])
  })

  it('returns undefined for a reply with no note', () => {
    expect(kiroAssistantTextSegments('A plain answer.')).toBeUndefined()
    expect(kiroAssistantTextSegments('')).toBeUndefined()
  })

  it('leaves a malformed marker in the text', () => {
    expect(kiroAssistantTextSegments('A [STEERING not-an-id: note] marker.')).toBeUndefined()
  })
})
