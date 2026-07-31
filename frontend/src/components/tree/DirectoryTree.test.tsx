import type { DirectoryTreeHandle } from './DirectoryTree'
import { render, waitFor } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { DirectoryTree } from './DirectoryTree'

const listDirectory = vi.fn()
vi.mock('~/api/workerRpc', () => ({
  listDirectory: (...args: unknown[]) => listDirectory(...args),
}))

function entry(root: string, name: string) {
  return { name, path: `${root}/${name}`, isDir: false, hidden: false }
}

function rowFor(name: string): Element | undefined {
  return [...document.querySelectorAll('[data-testid="tree-row"]')]
    .find(el => el.textContent?.includes(name))
}

describe('directoryTree', () => {
  /**
   * A background refresh must not re-create the rows it did not change.
   *
   * `<For>` maps by object REFERENCE, so replacing `childrenCache[path]` with a
   * fresh array of fresh objects disposed and rebuilt every sibling row — and
   * the three-dot menu is rendered INSIDE the row, so an open menu went with
   * it. One file written by an agent during a turn was enough to detach every
   * row in that directory at turn end, which is the race the e2e helpers'
   * open-then-click retry loop was written to survive.
   */
  it('keeps an unchanged row mounted when a refresh adds a sibling', async () => {
    const root = '/repo-reconcile'
    listDirectory.mockResolvedValue({
      entries: [entry(root, 'a.txt'), entry(root, 'b.txt')],
      truncated: false,
    })

    let handle!: DirectoryTreeHandle
    render(() => (
      <DirectoryTree
        workerId="w1"
        showFiles
        rootPath={root}
        selectedPath=""
        onSelect={() => {}}
        ref={(h) => { handle = h }}
      />
    ))
    await waitFor(() => expect(rowFor('a.txt')).toBeTruthy())
    const before = rowFor('a.txt')!

    listDirectory.mockResolvedValue({
      entries: [entry(root, 'a.txt'), entry(root, 'b.txt'), entry(root, 'c.txt')],
      truncated: false,
    })
    handle.refresh()
    await waitFor(() => expect(rowFor('c.txt')).toBeTruthy())

    expect(rowFor('a.txt')).toBe(before)
    expect(before.isConnected).toBe(true)
  })
})
