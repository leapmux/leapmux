import type { MessageRenderSources } from '~/components/chat/messageContextResolver'
import { NO_FILE_IMAGES } from '~/components/chat/fileImageResolver'

/** Supply only the related or live data that a renderer test needs. */
export function testMessageSources(overrides: Partial<MessageRenderSources> = {}): MessageRenderSources {
  return {
    current: () => undefined,
    request: () => undefined,
    result: () => undefined,
    role: () => 'other',
    visibleRows: () => ({ request: false, result: false }),
    fileImages: NO_FILE_IMAGES,
    backgroundTask: () => undefined,
    progress: () => undefined,
    ...overrides,
  }
}
