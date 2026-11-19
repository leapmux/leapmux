import type { OpenCodeToolContent } from '../toolResults.fixtures'
import { describe, expect, it } from 'vitest'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { pngBase64 } from '~/test-support/pngFixture'
import { providerToolCall } from '~/test-support/toolCallFixture'
import { openCodeToolPart } from '../toolResults.fixtures'
import '../plugin'

describe('openCodeExtractRow', () => {
  it.each(['completed', 'error'] as const)('keeps the %s projection unfinished on its opening stored row', (status) => {
    const content: OpenCodeToolContent[] = [
      { type: 'text', text: 'Recorded output' },
      { type: 'file', uri: `data:image/png;base64,${pngBase64}`, mime: 'image/png' },
    ]
    const metadata = {
      exit: status === 'error'
        ? 7
        : 0,
    }
    const projection = openCodeToolPart('call', 'shell', { command: 'printf output' }, status === 'error'
      ? {
          status,
          content,
          metadata,
          error: { type: 'failed', message: 'Native failure' },
        }
      : { status, content, metadata })
    const request = providerToolCall(AgentProvider.OPENCODE, projection, { spanId: 'call' })
    expect(request?.kind).toBe('execute')
    expect(request?.request).toMatchObject({ command: 'printf output' })
    expect(request?.status).toBe('in_progress')
    expect(request?.result).toBeUndefined()
    expect(request?.images).toEqual([])
    const result = providerToolCall(AgentProvider.OPENCODE, projection, {
      spanId: 'call',
      completion: status === 'error'
        ? MessageCompletion.ERROR
        : MessageCompletion.COMPLETE,
    })
    expect(result?.status).toBe(status === 'error'
      ? 'failed'
      : 'completed')
    expect(result?.result).toBeDefined()
    expect(result?.images).toHaveLength(1)
  })
})
