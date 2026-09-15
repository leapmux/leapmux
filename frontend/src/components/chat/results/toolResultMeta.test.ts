import type { ToolBodySource, ToolPresentation } from './toolPresentation'
import { describe, expect, it } from 'vitest'
import { toolBodyRepeatsInput, toolBodyStatesOwnOutcome, toolOutputCollapsible, toolPresentationMeta } from './toolResultMeta'

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

// Two EXHAUSTIVE predicates over the body union, not two arrays of literals.
// Array membership let a new body type join in silence, and these two decide
// whether the row repeats its input as raw JSON and whether it draws the shared
// outcome header -- which is the "generic wrench plus raw JSON" symptom the
// unification exists to remove.
describe('toolBodyRepeatsInput', () => {
  it('suppresses the JSON summary for a body that already draws the input', () => {
    for (const body of [
      { type: 'mcp', source: { server: 's', tool: 't', argsJson: '{}', content: [], status: 'success' } },
      { type: 'todo', items: [] },
      { type: 'markdown', text: '# plan' },
    ] as ToolBodySource[])
      expect(toolBodyRepeatsInput(body)).toBe(false)
  })

  it('keeps the JSON summary for every body that states nothing about the input', () => {
    for (const body of [
      { type: 'text' },
      { type: 'diff', sources: [] },
      { type: 'status', source: { title: 'Task output', outcome: 'succeeded', output: '' } },
    ] as ToolBodySource[])
      expect(toolBodyRepeatsInput(body)).toBe(true)
  })
})

describe('toolBodyStatesOwnOutcome', () => {
  it('suppresses the shared header for a body that draws its own failure notice', () => {
    for (const body of [
      { type: 'agent', source: { body: '' } },
      { type: 'command', source: { command: 'ls', output: '' } },
      { type: 'commands', entries: [] },
      { type: 'status', source: { title: 'Task output', outcome: 'failed', output: '' } },
    ] as ToolBodySource[])
      expect(toolBodyStatesOwnOutcome(body)).toBe(true)
  })

  it('draws the shared header above every body that states no outcome', () => {
    for (const body of [
      { type: 'text' },
      { type: 'diff', sources: [] },
      { type: 'mcp', source: { server: 's', tool: 't', argsJson: '{}', content: [], status: 'error' } },
      { type: 'todo', items: [] },
    ] as ToolBodySource[])
      expect(toolBodyStatesOwnOutcome(body)).toBe(false)
  })
})

// `hasCopyable` has to ANSWER truthfully, which means building the text -- and
// `copyableContent` is the same closure. Built twice, a diff row ran `diffLines`
// over every changed file once per streamed recompute and again on the click.
describe('toolPresentationMeta copyable cost', () => {
  it('builds the copyable text at most once', () => {
    let builds = 0
    const meta = toolPresentationMeta(presentation({ type: 'command', source: {
      command: 'ls',
      get output() {
        builds++
        return 'one\ntwo\n'
      },
    } } as unknown as ToolBodySource))

    expect(meta.hasCopyable).toBe(true)
    const first = builds
    expect(meta.copyableContent()).toContain('one')
    expect(meta.copyableContent()).toContain('one')
    expect(builds).toBe(first)
  })
})
