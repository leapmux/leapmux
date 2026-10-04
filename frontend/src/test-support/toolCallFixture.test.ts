import { describe, expect, it, vi } from 'vitest'
import { providerFor } from '~/components/chat/providers/registry'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerRow, toolCallFixture } from './toolCallFixture'
import '~/components/chat/providers'

/**
 * Every kind test uses this helper. Its output must satisfy the production model.
 * An override can explicitly supply undefined for a field that the helper removes.
 * A spread can replace default images with undefined. imagesForRow then throws and prevents the entire row list from rendering.
 * The production builder removes undefined values for the same reason.
 */
describe('toolCallFixture', () => {
  it('keeps the defaults when an override states undefined', () => {
    const call = toolCallFixture('read', { images: undefined, name: undefined, request: undefined })
    expect(call.images).toEqual([])
    expect(call.name).toBe('read')
    expect(call.request).toEqual({ path: '/p/a.ts' })
  })

  it('still applies an override that states a value', () => {
    const call = toolCallFixture('read', { name: 'Read', request: { path: '/repo/a.ts' }, images: [] })
    expect(call.name).toBe('Read')
    expect(call.request).toEqual({ path: '/repo/a.ts' })
  })

  it('gives the unspecified kind a displayable name', () => {
    expect(toolCallFixture('unspecified').name).toBe('tool')
  })

  // Invariant I2 requires a completed call to have a result.
  // Use the smallest valid result for each kind. A result-free test must use a status that allows no result.
  it('carries every field a call needs', () => {
    const call = toolCallFixture('execute')
    expect(Object.keys(call).sort()).toEqual(['id', 'images', 'kind', 'name', 'request', 'result', 'status'])
    expect(call.result).toStrictEqual({ commands: [], unresolvedTerminals: [] })
  })

  it('leaves the result off a call that has not finished', () => {
    const call = toolCallFixture('execute', { status: 'in_progress' })
    expect(call.result).toBeUndefined()
    expect(call.images).toStrictEqual([])
  })

  // The production builder validates the helper's output.
  // Other tests can then trust each fixture that they give to a renderer.
  it('refuses to build a call that breaks an invariant', () => {
    expect(() => toolCallFixture('read', { status: 'pending', result: { lines: null, fallbackContent: '' } }))
      .toThrow('result-before-the-call-finished')
    expect(() => toolCallFixture('edit', { request: { changes: [] } })).toThrow('a-file-change-states-no-file')
  })
})

describe('providerRow', () => {
  it('rejects an unregistered provider instead of returning no row', () => {
    expect(() => providerRow(AgentProvider.UNSPECIFIED, { fixture: 'neutral' }))
      .toThrow('The provider row fixture requires a registered plugin.')
  })

  it('keeps no row from a registered extractor', () => {
    const plugin = providerFor(AgentProvider.CLAUDE_CODE)
    if (!plugin)
      throw new Error('The fixture provider is not registered.')
    const classify = vi.spyOn(plugin.transcript, 'classify').mockReturnValue({ kind: 'assistant_text' })
    const extract = vi.spyOn(plugin.transcript, 'extractRow').mockReturnValue(null)
    try {
      expect(providerRow(AgentProvider.CLAUDE_CODE, { fixture: 'neutral' })).toBeNull()
      expect(extract).toHaveBeenCalledOnce()
    }
    finally {
      extract.mockRestore()
      classify.mockRestore()
    }
  })

  it.each([
    undefined,
    MessageCompletion.UNSPECIFIED,
    MessageCompletion.COMPLETE,
    MessageCompletion.INTERRUPTED,
    MessageCompletion.ERROR,
  ])('supplies completion metadata to classification and extraction: %s', (completion) => {
    const plugin = providerFor(AgentProvider.CLAUDE_CODE)
    if (!plugin)
      throw new Error('The fixture provider is not registered.')
    const classify = vi.spyOn(plugin.transcript, 'classify').mockReturnValue({ kind: 'assistant_text' })
    const extract = vi.spyOn(plugin.transcript, 'extractRow').mockReturnValue({ kind: 'assistant-text', text: 'Neutral fixture text.' })
    try {
      expect(providerRow(AgentProvider.CLAUDE_CODE, { fixture: 'neutral' }, completion === undefined ? {} : { completion }))
        .toEqual({ kind: 'assistant-text', text: 'Neutral fixture text.' })
      expect(classify).toHaveBeenCalledOnce()
      expect(extract).toHaveBeenCalledOnce()
      expect(classify.mock.calls[0]?.[0].completion).toBe(completion)
      expect(extract.mock.calls[0]?.[0].resolved.completion).toBe(completion)
      expect(Object.hasOwn(classify.mock.calls[0]?.[0] ?? {}, 'completion')).toBe(completion !== undefined)
    }
    finally {
      extract.mockRestore()
      classify.mockRestore()
    }
  })
})
