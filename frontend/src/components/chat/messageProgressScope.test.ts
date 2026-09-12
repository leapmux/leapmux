import { describe, expect, it } from 'vitest'
import { parseMessageContent } from '~/lib/messageParser'
import { createToolProgressStore } from '~/stores/chatToolProgress'
import { testMessageContext } from '~/test-support/messageContext'
import { makeMessage } from '~/test-support/messageFactory'
import { createMessageRenderSources } from './messageContextResolver'

describe('message progress scope', () => {
  it('does not attach current progress to a tool from an older provider session', () => {
    const progress = createToolProgressStore()
    progress.apply('agent', { spanId: 'reused-tool', elapsedSeconds: 30 })
    const options = {
      agentSessionId: () => 'current-session',
      progress: (spanId: string) => progress.get('agent', spanId),
    }
    const context = testMessageContext(options)
    const previous = makeMessage({ spanId: 'reused-tool', agentSessionId: 'previous-session' })
    const current = makeMessage({ spanId: 'reused-tool', agentSessionId: 'current-session' })
    const previousSources = createMessageRenderSources(() => context, () => previous, () => parseMessageContent(previous))
    const currentSources = createMessageRenderSources(() => context, () => current, () => parseMessageContent(current))
    expect(previousSources.progress()).toBeUndefined()
    expect(currentSources.progress()?.elapsedSeconds).toBe(30)
    progress.apply('agent', { spanId: 'reused-tool', elapsedSeconds: 0 })
    expect(currentSources.progress()?.elapsedSeconds).toBe(0)
    expect(previousSources.progress()).toBeUndefined()
  })
})
