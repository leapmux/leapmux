import type { FileEditDiff } from '../../model/fileEditDiff'
import { render } from '@solidjs/testing-library'
import { beforeAll, describe, expect, it } from 'vitest'
import { checkKindModule } from '~/test-support/kindTestHarness'
import { toolCallFixture, toolRow } from '~/test-support/toolCallFixture'
import { ToolMessage } from '../ToolMessage'

// jsdom does not provide ResizeObserver, which the shared layouts observe with.
beforeAll(() => {
  globalThis.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver
})

describe('write renderer', () => {
  checkKindModule({
    kind: 'write',
    request: { changes: [{ filePath: '/p/new.ts', oldStr: '', newStr: 'one\n', structuredPatch: null, operation: 'add' as const }] },
    titlePart: 'new.ts',
    result: { changes: [{ filePath: '/p/new.ts', oldStr: '', newStr: 'one\n', structuredPatch: null, operation: 'add' as const }] },
  })
})

function renderWrite(changes: FileEditDiff[]) {
  const call = toolCallFixture('write', { request: { changes }, result: { changes } })
  return render(() => <ToolMessage row={toolRow(call)} />)
}

describe('writeRenderer', () => {
  // The line count takes the place of the diff badge. An E2E locator that
  // requires the badge (`fileChangeRow`) therefore never finds this row.
  it('titles a write that creates one file with its line count and no diff badge', () => {
    const view = renderWrite([{ filePath: '/p/new.ts', oldStr: '', newStr: 'one\ntwo\n', structuredPatch: null, operation: 'add' }])

    expect(view.container.textContent).toContain('/p/new.ts (2 lines)')
    expect(view.queryByTestId('git-diff-stats')).toBeNull()
    // The diff body still draws the lines that the write added.
    const diff = view.container.querySelector('[data-file-diff][data-file-path="/p/new.ts"]')
    expect(diff?.textContent).toContain('one')
    expect(diff?.textContent).toContain('two')
  })

  it('states no line count for a write that creates an empty file', () => {
    const view = renderWrite([{ filePath: '/p/empty.ts', oldStr: '', newStr: '', structuredPatch: null, operation: 'add' }])

    expect(view.container.textContent).toContain('/p/empty.ts')
    expect(view.container.textContent).not.toMatch(/\(\d+ lines?\)/)
    expect(view.queryByTestId('git-diff-stats')).toBeNull()
  })

  // Only an add states a whole-file count. A write that replaces part of an
  // existing file takes the title that every other file change takes.
  it('titles a write that changes an existing file with the diff badge', () => {
    const view = renderWrite([{ filePath: '/p/old.ts', oldStr: 'before\n', newStr: 'after\n', operation: 'edit' }])

    expect(view.container.textContent).toContain('/p/old.ts')
    expect(view.container.textContent).not.toMatch(/\(\d+ lines?\)/)
    const badge = view.getByTestId('git-diff-stats')
    expect(badge.textContent).toContain('+1')
    expect(badge.textContent).toContain('-1')
  })
})
