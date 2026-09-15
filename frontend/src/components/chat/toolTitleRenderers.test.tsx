import type { ToolKind } from './results/toolKind'
import type { ToolPresentation } from './results/toolPresentation'
import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { TOOL_KINDS } from './results/toolKind'
import { renderEditTitle, renderReadTitle, renderWriteTitle, TITLED_TOOL_KINDS, toolMessageTitle } from './toolTitleRenderers'

describe('file tool titles', () => {
  it.each([-1, 1.5, Number.NaN, Number.POSITIVE_INFINITY, Number.MAX_SAFE_INTEGER + 1])('ignores an invalid read offset or limit: %s', (value) => {
    const { container } = render(() => renderReadTitle('/file.ts', value, value))
    expect(container.textContent).toBe('/file.ts')
  })

  it('does not display an end line that exceeds the safe integer range', () => {
    const { container } = render(() => renderReadTitle('/file.ts', Number.MAX_SAFE_INTEGER, 2))
    expect(container.textContent).toBe(`/file.ts (Line ${Number.MAX_SAFE_INTEGER}–)`)
  })

  it('counts inserted lines when the original text is empty', () => {
    const { container } = render(() => renderEditTitle('/file.ts', '', 'one\ntwo\n'))
    expect(container.textContent).toContain('+2')
  })

  it('counts removed lines when the new text is empty', () => {
    const { container } = render(() => renderEditTitle('/file.ts', 'one\ntwo\n', ''))
    expect(container.textContent).toMatch(/[-−]2/)
  })

  it('does not count a trailing newline as another written line', () => {
    const { container } = render(() => renderWriteTitle('/file.ts', 'one\n'))
    expect(container.textContent).toContain('(1 line)')
  })
})

// `ToolMessage` repeats a tool's input as JSON for every kind OUTSIDE
// `TITLED_TOOL_KINDS`. The set and the switch below it are two statements of one
// fact, and a kind in one but not the other either loses its input or states it
// twice. This test is what keeps them in step. See DESIGN-FE-2.
describe('toolMessageTitle coverage', () => {
  // One input that every title renderer can read, so the only variable is the kind.
  const probe = (kind: ToolKind): ToolPresentation => ({
    kind,
    title: 'RAW TITLE',
    input: {
      filePath: '/project/src/a.ts',
      content: 'one\n',
      pattern: '*.ts',
      query: 'needle',
      url: 'https://example.com',
      command: 'ls',
      description: 'List the files',
    },
    output: '',
    body: { type: 'text' },
    unresolvedTerminals: [],
  })

  it.each(TOOL_KINDS)('writes a title for %j exactly when the set claims one', (kind) => {
    const title = toolMessageTitle(probe(kind), { workingDir: '/project' })
    expect(typeof title === 'string' ? title : '').toBe(TITLED_TOOL_KINDS.has(kind) ? '' : 'RAW TITLE')
  })

  it('keeps the tool title for a move whose input carries no path it knows', () => {
    const model = probe('move')
    model.input = { source_path: '/project/a.ts', destination_path: '/project/b.ts' }
    expect(toolMessageTitle(model, { workingDir: '/project' })).toBe('RAW TITLE')
  })

  it('shows the destination of a move that carries a path it knows', () => {
    const { container } = render(() => toolMessageTitle(probe('move'), { workingDir: '/project' }))
    expect(container.textContent).toContain('src/a.ts')
  })
})
