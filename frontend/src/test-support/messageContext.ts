import type { MessageContextSources } from '~/components/chat/messageContextResolver'
import { createMemo, createRoot } from 'solid-js'
import { afterEach } from 'vitest'
import { createMessageContextResolver } from '~/components/chat/messageContextResolver'
import { createSpanIndex } from '~/stores/chatSpanIndex'

const cleanups: Array<() => void> = []
afterEach(() => cleanups.splice(0).forEach(dispose => dispose()))

/** Exercise the real resolver with controlled transcript and transport sources. */
export function testMessageContext(overrides: Partial<MessageContextSources> = {}) {
  return createRoot((dispose) => {
    cleanups.push(dispose)
    const messages = overrides.messages ?? (() => [])
    const index = createMemo(() => {
      const spans = createSpanIndex()
      spans.reindex('test', messages())
      return spans
    })
    return createMessageContextResolver({
      scopeKey: () => 'test',
      messages,
      messageVersion: () => 0,
      contentVersion: () => 0,
      messageBySeq: seq => messages().find(message => message.seq === seq),
      spanMessage: (spanId, side) => side === 'request' ? index().getOpenerMessage('test', spanId) : index().getResultMessage('test', spanId),
      fetchMessage: async () => undefined,
      fetchSpan: async () => [],
      fetchFileImage: async () => { throw new Error('The image source is unavailable') },
      subscribe: () => () => undefined,
      todo: () => undefined,
      backgroundTask: () => undefined,
      progress: () => undefined,
      ...overrides,
    })
  })
}
