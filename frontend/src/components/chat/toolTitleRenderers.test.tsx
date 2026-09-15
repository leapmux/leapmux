import type { ToolKind } from './results/toolKind'
import type { ToolPresentation } from './results/toolPresentation'
import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { TOOL_KINDS } from './results/toolKind'
import { kindHasTitleRenderer, renderEditTitle, renderReadTitle, renderWriteTitle, toolMessageTitle } from './toolTitleRenderers'

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

// `ToolMessage` repeats a tool's input as JSON for every kind that
// `kindHasTitleRenderer` answers false for. Both answers now come from ONE table,
// `TOOL_TITLE_RENDERERS`, which the compiler checks for coverage -- so this test
// no longer holds two tables in step. It states the OBSERVABLE rule instead: a
// kind with a renderer draws something other than the raw tool title.
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

  it.each(TOOL_KINDS)('writes a title for %j exactly when it claims a renderer', (kind) => {
    const title = toolMessageTitle(probe(kind), { workingDir: '/project' })
    expect(typeof title === 'string' ? title : '').toBe(kindHasTitleRenderer(kind) ? '' : 'RAW TITLE')
  })

  // Reasonix's `move_file` sends these two keys and no `filePath`. The row is in
  // this state while the move runs, and after one that failed or was cancelled;
  // a move that COMPLETED takes the diff branch above the table instead.
  it('shows both paths of a move that states a source and a destination', () => {
    const model = probe('move')
    model.input = { source_path: '/project/a.ts', destination_path: '/project/b.ts' }
    const { container } = render(() => toolMessageTitle(model, { workingDir: '/project' }))
    expect(container.textContent).toContain('a.ts')
    expect(container.textContent).toContain('b.ts')
  })

  it('shows the one path of a move that resolved a source alone', () => {
    const model = probe('move')
    model.input = { source_path: '/project/a.ts' }
    const { container } = render(() => toolMessageTitle(model, { workingDir: '/project' }))
    expect(container.textContent).toContain('a.ts')
  })

  it('keeps the tool title for a move that resolved no path at all', () => {
    const model = probe('move')
    model.input = {}
    expect(toolMessageTitle(model, { workingDir: '/project' })).toBe('RAW TITLE')
  })

  it('shows the destination of a move that carries a path it knows', () => {
    const { container } = render(() => toolMessageTitle(probe('move'), { workingDir: '/project' }))
    expect(container.textContent).toContain('src/a.ts')
  })

  // ZCode's `Edit` states `replace_all`, and the difference between replacing one
  // occurrence and every one is what makes the call worth reading before it runs.
  it('marks an edit that replaced every occurrence', () => {
    const model = probe('edit')
    model.input = { filePath: '/project/src/a.ts', old_string: 'one', new_string: 'two' }
    model.replaceAll = true
    const { container } = render(() => toolMessageTitle(model, { workingDir: '/project' }))
    expect(container.textContent).toContain('(replace all)')
  })

  it('does not mark an edit that replaced one occurrence', () => {
    const model = probe('edit')
    model.input = { filePath: '/project/src/a.ts', old_string: 'one', new_string: 'two' }
    const { container } = render(() => toolMessageTitle(model, { workingDir: '/project' }))
    expect(container.textContent).not.toContain('replace all')
  })
})
