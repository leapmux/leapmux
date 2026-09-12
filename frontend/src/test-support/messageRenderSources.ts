import type { MessageRenderSources } from '~/components/chat/messageContextResolver'

/** Supply only the related or live data that a renderer test needs. */
export function testMessageSources(overrides: Partial<MessageRenderSources> = {}): MessageRenderSources {
  return {
    current: () => undefined,
    request: () => undefined,
    result: () => undefined,
    role: () => 'other',
    fileImage: async () => { throw new Error('The image source is unavailable') },
    cachedFileImage: () => undefined,
    todo: () => undefined,
    backgroundTask: () => undefined,
    progress: () => undefined,
    ...overrides,
  }
}
