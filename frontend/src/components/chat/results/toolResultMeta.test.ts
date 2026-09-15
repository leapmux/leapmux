import type { ToolBodySource, ToolPresentation } from './toolPresentation'
import { describe, expect, it } from 'vitest'
import { toolOutputCollapsible, toolPresentationMeta } from './toolResultMeta'

function presentation(body: ToolBodySource, overrides: Partial<ToolPresentation> = {}): ToolPresentation {
  return {
    kind: 'edit',
    title: 'Tool',
    input: {},
    output: '',
    body,
    unresolvedTerminals: [],
    ...overrides,
  }
}

describe('toolPresentationMeta hasCopyable', () => {
  // `hasCopyable` states that `copyableContent()` returns a string. A diff body
  // answered `true` whatever it held, so the Copy button on a no-op edit wrote
  // no clipboard entry, flashed no confirmation and reported no error.
  it('refuses a diff whose edit changes nothing', () => {
    const meta = toolPresentationMeta(presentation({
      type: 'diff',
      sources: [{ filePath: '/project/a.ts', oldStr: 'same', newStr: 'same', structuredPatch: null }],
    }))
    expect(meta.copyableContent()).toBeNull()
    expect(meta.hasCopyable).toBe(false)
  })

  it('refuses a streaming diff row that carries no text yet', () => {
    const meta = toolPresentationMeta(presentation({
      type: 'diff',
      sources: [{ filePath: '/project/a.ts', oldStr: '', newStr: '', structuredPatch: null }],
    }))
    expect(meta.copyableContent()).toBeNull()
    expect(meta.hasCopyable).toBe(false)
  })

  it('admits a diff that holds a real change', () => {
    const meta = toolPresentationMeta(presentation({
      type: 'diff',
      sources: [{ filePath: '/project/a.ts', oldStr: 'before\n', newStr: 'after\n', structuredPatch: null }],
    }))
    expect(meta.copyableContent()).toContain('after')
    expect(meta.hasCopyable).toBe(true)
  })

  it('admits a diff that changes nothing beside MCP content that does', () => {
    const meta = toolPresentationMeta(presentation(
      { type: 'diff', sources: [{ filePath: '/project/a.ts', oldStr: 'same', newStr: 'same', structuredPatch: null }] },
      {
        additionalContent: {
          server: 'srv',
          tool: 'tool',
          argsJson: '',
          status: 'completed',
          content: [{ type: 'text', text: 'extra output' }],
        },
      },
    ))
    expect(meta.hasCopyable).toBe(true)
    expect(meta.copyableContent()).toBe('extra output')
  })

  it('agrees with copyableContent for a plain text body', () => {
    const empty = toolPresentationMeta(presentation({ type: 'text' }))
    expect(empty.hasCopyable).toBe(false)
    const filled = toolPresentationMeta(presentation({ type: 'text' }, { output: 'result text' }))
    expect(filled.hasCopyable).toBe(true)
    expect(filled.copyableContent()).toBe('result text')
  })
})

describe('toolOutputCollapsible', () => {
  it('reads a web fetch result through the plain output rule', () => {
    const long = Array.from({ length: 40 }, (_, index) => `line ${index}`).join('\n')
    const source = { url: 'https://example.com', result: '' }
    expect(toolOutputCollapsible(presentation({ type: 'fetch', source }, { output: long }))).toBe(true)
    expect(toolOutputCollapsible(presentation({ type: 'fetch', source }, { output: 'one line' }))).toBe(false)
  })

  it('reads a read body with no line list through the plain output rule', () => {
    const long = Array.from({ length: 40 }, (_, index) => `line ${index}`).join('\n')
    expect(toolOutputCollapsible(presentation({ type: 'read', source: { filePath: '/project/a.ts', lines: null, totalLines: 0, numLines: 0, fallbackContent: '' } }, { output: long }))).toBe(true)
  })
})

describe('toolPresentationMeta status body', () => {
  const status = { title: 'Stopped task task-42', outcome: 'stopped' as const, output: 'The task stopped.' }

  // The presentation's own output is the RAW result text, which the status header
  // replaced with the words the reader sees.
  it('copies the note the status header holds, not the raw result text', () => {
    const meta = toolPresentationMeta(presentation({ type: 'status', source: status }, { output: 'raw provider text' }))
    expect(meta.copyableContent()).toBe('The task stopped.')
    expect(meta.hasCopyable).toBe(true)
  })

  it('reads the note through the plain collapse rule', () => {
    const long = Array.from({ length: 40 }, (_, index) => `line ${index}`).join('\n')
    expect(toolOutputCollapsible(presentation({ type: 'status', source: { ...status, output: long } }))).toBe(true)
    expect(toolOutputCollapsible(presentation({ type: 'status', source: status }))).toBe(false)
  })
})

describe('toolPresentationMeta todo body', () => {
  const items = [{ id: '1', rowKey: '1', content: 'Inspect the sample', status: 'pending' as const, activeForm: 'Inspecting the sample' }]

  it('copies the checklist and the note the row draws below it', () => {
    const meta = toolPresentationMeta(presentation({ type: 'todo', items, description: 'Read the entry points.' }))
    expect(meta.copyableContent()).toContain('Inspect the sample')
    expect(meta.copyableContent()).toContain('Read the entry points.')
  })

  it('copies the checklist alone when the row draws no note', () => {
    const meta = toolPresentationMeta(presentation({ type: 'todo', items }))
    expect(meta.copyableContent()).toContain('Inspect the sample')
    expect(meta.copyableContent()).not.toContain('\n\n')
  })

  // The whole list fits the row, so an expand button would have nothing to expand.
  it('never collapses a checklist', () => {
    expect(toolOutputCollapsible(presentation({ type: 'todo', items }))).toBe(false)
  })
})
