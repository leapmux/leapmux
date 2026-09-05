import { create } from '@bufbuild/protobuf'
import { describe, expect, it, vi } from 'vitest'
import { AgentChatMessageSchema } from '~/generated/proto/leapmux/v1/agent_pb'
import {
  applyFreshMessage,
  firstMessageSeq,
  insertMessageBySeq,
  isReapablePhantom,
  lastMessageSeq,
  mergeWindow,
  prunableDroppedSpanIds,
  transcriptMessageEnd,
  transcriptMessageStart,
} from '~/stores/chatMessageOrder'

function msg(id: string, seq: bigint, spanId = '') {
  return create(AgentChatMessageSchema, { id, seq, spanId })
}
const ids = (list: { id: string }[]) => list.map(message => message.id)

describe('chatMessageOrder', () => {
  it('reads the server range directly from a positive-sequence window', () => {
    const messages = [msg('a', 1n), msg('b', 2n)]
    expect(transcriptMessageStart(messages)).toBe(0)
    expect(transcriptMessageEnd(messages)).toBe(2)
    expect(firstMessageSeq(messages)).toBe(1n)
    expect(lastMessageSeq(messages)).toBe(2n)
    expect(firstMessageSeq([])).toBeUndefined()
    expect(lastMessageSeq([])).toBeUndefined()
  })

  it('classifies the catch-up phantom band', () => {
    expect(isReapablePhantom(10n, 10n)).toBe(false)
    expect(isReapablePhantom(11n, 10n)).toBe(true)
    expect(isReapablePhantom(15n, 10n, 20n)).toBe(true)
    expect(isReapablePhantom(21n, 10n, 20n)).toBe(false)
  })

  it('inserts messages in sequence order', () => {
    expect(ids(insertMessageBySeq([msg('a', 1n), msg('c', 3n)], msg('b', 2n))))
      .toEqual(['a', 'b', 'c'])
  })

  it('keeps a surviving shared span', () => {
    expect(prunableDroppedSpanIds(
      [msg('opener', 1n, 'shared'), msg('old', 2n, 'old')],
      [msg('result', 3n, 'shared')],
    )).toEqual(['old'])
  })

  it('inserts a fresh row and discards a duplicate sequence', () => {
    const previous = [msg('a', 1n), msg('c', 3n)]
    const inserted = applyFreshMessage(previous, msg('b', 2n))
    expect(inserted.inserted).toBe(true)
    expect(ids(inserted.next)).toEqual(['a', 'b', 'c'])

    const duplicate = applyFreshMessage(inserted.next, msg('other', 2n))
    expect(duplicate.inserted).toBe(false)
    expect(duplicate.next).toBe(inserted.next)
  })

  it('merges older and newer pages without changing an identical window', () => {
    const previous = [msg('c', 3n), msg('d', 4n)]
    expect(ids(mergeWindow(previous, [msg('a', 1n), msg('b', 2n)], 'older')))
      .toEqual(['a', 'b', 'c', 'd'])
    expect(ids(mergeWindow(previous, [msg('e', 5n)], 'newer')))
      .toEqual(['c', 'd', 'e'])
    expect(mergeWindow(previous, [msg('c', 3n), msg('d', 4n)], 'newer')).toBe(previous)
  })

  it('rejects an overlapping older page in development', () => {
    expect(() => mergeWindow([msg('c', 3n)], [msg('x', 5n)], 'older'))
      .toThrow(/overlaps the window head/)
  })

  it('uses a sequence insert for an overlapping older page in production', () => {
    vi.stubEnv('DEV', false)
    try {
      expect(ids(mergeWindow([msg('c', 3n)], [msg('x', 5n)], 'older')))
        .toEqual(['c', 'x'])
    }
    finally {
      vi.unstubAllEnvs()
    }
  })

  it('replaces a stable ID after a resequence', () => {
    const merged = mergeWindow(
      [msg('a', 1n), msg('b', 2n), msg('c', 3n)],
      [msg('b', 9n)],
      'newer',
    )
    expect(ids(merged)).toEqual(['a', 'c', 'b'])
    expect(merged[2]?.seq).toBe(9n)
  })
})
