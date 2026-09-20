import type { TranscriptFrame } from '~/test-support/messageFactory'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { makeTranscriptMessage, rawContent } from '~/test-support/messageFactory'

describe('makeTranscriptMessage', () => {
  it('encodes structured content and preserves raw content bytes', () => {
    const structured = makeTranscriptMessage({
      id: 'structured',
      provider: AgentProvider.CODEX,
      content: { item: { type: 'reasoning', text: 'Inspect' } },
    }, 1n)
    const bytes = rawContent({ captured: true })
    const captured = makeTranscriptMessage({
      id: 'captured',
      provider: AgentProvider.CODEX,
      rawContent: bytes,
    }, 2n)

    expect(JSON.parse(new TextDecoder().decode(structured.content))).toEqual({ item: { type: 'reasoning', text: 'Inspect' } })
    expect(captured.content).toBe(bytes)
  })

  it.each([
    {
      label: 'no payload source',
      frame: { id: 'missing-content', provider: AgentProvider.CODEX },
    },
    {
      label: 'both payload sources',
      frame: { id: 'duplicate-content', provider: AgentProvider.CODEX, content: {}, rawContent: rawContent({}) },
    },
  ])('rejects $label at a JavaScript boundary', ({ frame }) => {
    expect(() => makeTranscriptMessage(frame as TranscriptFrame, 1n)).toThrow('exactly one')
  })
})
