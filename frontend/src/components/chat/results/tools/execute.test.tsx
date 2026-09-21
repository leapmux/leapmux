import type { ExecuteRequest } from '../../model/tools/execute'
import { fireEvent, render } from '@solidjs/testing-library'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { hoverForTooltip } from '~/test-support/clipStub'
import { classSelector } from '~/test-support/composedClass'
import { checkKindModule } from '~/test-support/kindTestHarness'
import { toolCallFixture, toolRow } from '~/test-support/toolCallFixture'
import { commandActionCodeText, toolBodyContent } from '../../toolStyles.css'
import { ToolMessage } from '../ToolMessage'

// jsdom does not provide ResizeObserver, which the shared layouts observe with.
beforeAll(() => {
  globalThis.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver
})

afterEach(() => {
  vi.useRealTimers()
})

describe('execute renderer', () => {
  checkKindModule({
    kind: 'execute',
    request: { command: 'ls', description: 'List files' },
    titlePart: 'List files',
    result: { commands: [{ output: 'a.ts' }], unresolvedTerminals: [] },
    resultPart: 'a.ts',
  })

  // The command belongs to the row that states the REQUEST. Where the request is a
  // row of its own, a result row that repeated the command would draw it twice.
  describe('the command line', () => {
    const call = toolCallFixture('execute', {
      request: { command: 'rg --files' },
      result: { commands: [{ output: 'a.ts' }], unresolvedTerminals: [] },
    })

    it('stays off a result row whose request is beside it', () => {
      const { container } = render(() => <ToolMessage row={toolRow(call, 'result', { request: true })} />)
      expect(container.textContent).not.toContain('rg --files')
      expect(container.textContent).toContain('a.ts')
    })

    it('keeps command actions and process metadata off a paired result row', () => {
      const paired = toolCallFixture('execute', {
        request: {
          command: 'cat a.ts',
          processId: '79860',
          actions: [{ kind: 'read', command: 'cat a.ts', name: 'a.ts', path: '/repo/a.ts' }],
        },
        result: { commands: [{ output: 'file body' }], unresolvedTerminals: [] },
      })
      const { container } = render(() => <ToolMessage row={toolRow(paired, 'result', { request: true })} />)

      expect(container.querySelector('[data-command-action]')).toBeNull()
      expect(container).not.toHaveTextContent('Process ID:')
      expect(container).toHaveTextContent('file body')
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

  describe('the command action list', () => {
    const request = {
      command: '/bin/zsh -lc "sed and rg"',
      cwd: '/repo',
      processId: '79860',
      actions: [
        { kind: 'read' as const, command: 'sed -n \'380,430p\' frontend/tests/e2e/helpers/ui.ts', name: 'ui.ts', path: '/repo/frontend/tests/e2e/helpers/ui.ts' },
        { kind: 'list' as const, command: 'rg --files frontend/tests', path: 'frontend/tests' },
        { kind: 'search' as const, command: 'rg -n \'loginViaToken\' frontend/tests/e2e', query: 'loginViaToken', path: 'frontend/tests/e2e' },
        { kind: 'unknown' as const, command: 'head -100' },
      ],
    }

    it('draws known actions as descriptions and unknown actions as commands', () => {
      const call = toolCallFixture('execute', { request })
      const { container } = render(() => <ToolMessage row={toolRow(call)} context={{ workingDir: '/repo' }} />)
      const items = [...container.querySelectorAll('li')].map(item => item.textContent)

      expect(items).toEqual([
        'Read frontend/tests/e2e/helpers/ui.ts',
        'List files in frontend/tests',
        'Search for "loginViaToken" in frontend/tests/e2e',
        'head -100',
      ])
      expect(container).toHaveTextContent('Working directory:')
      expect(container).toHaveTextContent('Process ID:')
      expect(container).toHaveTextContent('79860')
      expect(container).not.toHaveTextContent('sed -n \'380,430p\'')
    })

    it('draws file names and search terms in the monospace styles', () => {
      const call = toolCallFixture('execute', { request })
      const { container } = render(() => <ToolMessage row={toolRow(call)} context={{ workingDir: '/repo' }} />)
      const codeText = [...container.querySelectorAll(classSelector(commandActionCodeText))].map(element => element.textContent)

      expect(codeText).toContain('frontend/tests/e2e/helpers/ui.ts')
      expect(codeText).toContain('frontend/tests/e2e')
      expect(codeText).toContain('"loginViaToken"')
    })

    it('removes the bullet when the request contains one action', () => {
      const call = toolCallFixture('execute', {
        title: 'Inspect source',
        request: {
          command: 'cat a.ts',
          actions: [{ kind: 'read', command: 'cat a.ts', name: 'a.ts', path: '/repo/a.ts' }],
        },
      })
      const { container } = render(() => <ToolMessage row={toolRow(call)} context={{ workingDir: '/repo' }} />)

      expect(container.querySelector('[data-command-action="read"]')).not.toBeNull()
      expect(container.querySelector('ul')).toBeNull()
      expect(container.querySelector('li')).toBeNull()
    })

    it('shows the raw command for a known action in a tooltip', () => {
      vi.useFakeTimers()
      const call = toolCallFixture('execute', { request })
      const { container } = render(() => <ToolMessage row={toolRow(call)} context={{ workingDir: '/repo' }} />)
      const read = container.querySelector('[data-command-action="read"]')

      expect(read).not.toBeNull()
      expect(hoverForTooltip(read!)?.textContent).toBe('sed -n \'380,430p\' frontend/tests/e2e/helpers/ui.ts')
    })

    it('keeps the raw command fallback when no actions exist', () => {
      const call = toolCallFixture('execute', { request: { command: 'printf raw-fallback', cwd: '/repo', processId: '7' } })
      const { container } = render(() => <ToolMessage row={toolRow(call)} context={{ workingDir: '/repo' }} />)

      expect(container).toHaveTextContent('printf raw-fallback')
      expect(container.querySelector('ul')).toBeNull()
      expect(container).toHaveTextContent('Process ID:')
      expect(container).toHaveTextContent('7')
    })

    it('offers action expansion when the collapsed list overflows', async () => {
      const scroll = vi.spyOn(HTMLElement.prototype, 'scrollHeight', 'get').mockReturnValue(120)
      const client = vi.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockReturnValue(40)
      try {
        const call = toolCallFixture('execute', { request })
        const { container } = render(() => <ToolMessage row={toolRow(call)} />)
        await new Promise(resolve => setTimeout(resolve, 0))
        const expand = container.querySelector<HTMLButtonElement>('[aria-label="Show all actions"]')

        expect(expand).not.toBeNull()
        fireEvent.click(expand!)
        expect(container.querySelector('[aria-label="Collapse"]')).not.toBeNull()
      }
      finally {
        scroll.mockRestore()
        client.mockRestore()
      }
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
  const headerOf = (request: ExecuteRequest, title?: string) => {
    const { container } = render(() => <ToolMessage row={toolRow(toolCallFixture('execute', { request, title }))} />)
    return container.querySelector('[data-testid="execute-title"]')?.textContent ?? ''
  }

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

  it('moves one known action description into the title and omits its body copy', () => {
    vi.useFakeTimers()
    const command = 'sed -n \'1,5p\' src/main.ts'
    const call = toolCallFixture('execute', {
      title: 'Run command',
      request: {
        command: '/bin/zsh -lc "sed"',
        actions: [{ kind: 'read', command, name: 'main.ts', path: '/repo/src/main.ts' }],
      },
    })
    const { container } = render(() => <ToolMessage row={toolRow(call)} context={{ workingDir: '/repo' }} />)
    const title = container.querySelector('[data-testid="execute-title"]')

    expect(title).toHaveTextContent('Read src/main.ts')
    expect(container.querySelector('[data-command-action]')).toBeNull()
    expect(container.querySelector(classSelector(toolBodyContent))).toBeNull()
    expect(hoverForTooltip(title!)?.textContent).toBe(command)
  })

  it('keeps a specific frame title and leaves the known action in the body', () => {
    const call = toolCallFixture('execute', {
      title: 'Inspect source',
      request: {
        command: '/bin/zsh -lc "sed"',
        actions: [{ kind: 'read', command: 'sed -n \'1,5p\' src/main.ts', name: 'main.ts', path: '/repo/src/main.ts' }],
      },
    })
    const { container } = render(() => <ToolMessage row={toolRow(call)} context={{ workingDir: '/repo' }} />)

    expect(container.querySelector('[data-testid="execute-title"]')).toHaveTextContent('Inspect source')
    expect(container.querySelector('[data-command-action="read"]')).not.toBeNull()
  })

  it('keeps an unknown single action under the generic title', () => {
    const call = toolCallFixture('execute', {
      title: 'Run command',
      request: {
        command: 'printf raw-action',
        actions: [{ kind: 'unknown', command: 'printf raw-action' }],
      },
    })
    const { container } = render(() => <ToolMessage row={toolRow(call)} />)

    expect(container.querySelector('[data-testid="execute-title"]')).toHaveTextContent('Run command')
    expect(container.querySelector('[data-command-action="unknown"]')).toHaveTextContent('printf raw-action')
  })

  it('uses Run commands when the request contains more than one action', () => {
    expect(headerOf({
      command: 'cat a.ts && cat b.ts',
      actions: [
        { kind: 'read', command: 'cat a.ts', name: 'a.ts', path: '/repo/a.ts' },
        { kind: 'read', command: 'cat b.ts', name: 'b.ts', path: '/repo/b.ts' },
      ],
    })).toContain('Run commands')
  })
})
