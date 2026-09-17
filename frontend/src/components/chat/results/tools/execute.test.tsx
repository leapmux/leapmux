import { render } from '@solidjs/testing-library'
import { beforeAll, describe, expect, it } from 'vitest'
import { checkKindModule } from '~/test-support/kindTestHarness'
import { toolCallIr, toolRow } from '~/test-support/toolCallIr'
import { ToolMessage } from '../ToolMessage'

// jsdom does not provide ResizeObserver, which the shared layouts observe with.
beforeAll(() => {
  globalThis.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver
})

describe('execute renderer', () => {
  checkKindModule({
    kind: 'execute',
    request: { command: 'ls', description: 'List files' },
    titlePart: 'List files',
    result: { commands: [{ output: 'a.ts' }], unresolvedTerminals: [] },
    resultPart: 'a.ts',
  })

  // The command belongs to the row that states the REQUEST. Where the opener is a
  // row of its own, a result row that repeated the command would draw it twice.
  describe('the command line', () => {
    const call = toolCallIr('execute', {
      request: { command: 'rg --files' },
      result: { commands: [{ output: 'a.ts' }], unresolvedTerminals: [] },
    })

    it('stays off a result row whose opener is beside it', () => {
      const { container } = render(() => <ToolMessage row={toolRow(call, 'result', { request: true })} />)
      expect(container.textContent).not.toContain('rg --files')
      expect(container.textContent).toContain('a.ts')
    })

    it('stays on a lone result row, the one place the command is stated', () => {
      const { container } = render(() => <ToolMessage row={toolRow(call, 'result', { request: false })} />)
      expect(container.textContent).toContain('rg --files')
    })

    it('stays on an update row, which may be the only row the call has', () => {
      const { container } = render(() => <ToolMessage row={toolRow(call, 'update')} />)
      expect(container.textContent).toContain('rg --files')
    })
  })
})

/**
 * The header states what the command was FOR, never the command itself: the summary
 * below already draws the command line, and a title that repeated it would sit above
 * the very line it copies.
 *
 * `Run command` is the last resort, and it is the shape a dropped description takes on
 * screen -- Pi's extractor built its execute request without one, so every Pi command
 * row headed itself with those two words.
 */
describe('the execute row header (executeRenderer)', () => {
  const headerOf = (request: { command: string, description?: string }, title?: string) =>
    render(() => <ToolMessage row={toolRow(toolCallIr('execute', { request, title }))} />).container.textContent ?? ''

  it('states the description the agent sent', () => {
    expect(headerOf({ command: 'ls -la', description: 'List files in current directory' }))
      .toContain('List files in current directory')
  })

  it('clips a description long enough to crowd the header', () => {
    const long = 'x'.repeat(140)
    const header = headerOf({ command: 'ls', description: long })
    expect(header).toContain(`${'x'.repeat(100)}…`)
    expect(header).not.toContain('x'.repeat(101))
  })

  // The clip is `> DESCRIPTION_LIMIT`, so a description of exactly the limit is drawn
  // whole. An off-by-one here truncates a header that fits and marks a cut that the
  // reader cannot act on, because the full words are nowhere else on the row.
  it.each([[100, false], [101, true]])('clips a %i-character description: %s', (length, clipped) => {
    const header = headerOf({ command: 'ls', description: 'x'.repeat(length) })
    expect(header.includes('…')).toBe(clipped)
    expect(header).toContain('x'.repeat(100))
  })

  // EMPTY is absent, not a description. Drawing it would head the row with a blank
  // line, and the frame's own title -- the only other thing that identifies the call --
  // would never be reached.
  it('reads an empty description as no description at all', () => {
    expect(headerOf({ command: 'ls -la', description: '' }, 'Check the tree')).toContain('Check the tree')
  })

  it('falls back to the frame title, then to Run command, when the call states no description', () => {
    expect(headerOf({ command: 'ls -la' }, 'Check the tree')).toContain('Check the tree')
    expect(headerOf({ command: 'ls -la' })).toContain('Run command')
  })
})
