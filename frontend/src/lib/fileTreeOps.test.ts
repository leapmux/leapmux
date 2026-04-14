import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  refreshFileTree,
  registerDialogFileTreeOps,
  registerSidebarFileTreeOps,
  toggleHiddenFiles,
} from './fileTreeOps'

const unregisters: Array<() => void> = []

afterEach(() => {
  while (unregisters.length > 0)
    unregisters.pop()?.()
})

describe('fileTreeOps', () => {
  it('dispatches to the last registered dialog ops', () => {
    const first = { refresh: vi.fn(), toggleHiddenFiles: vi.fn() }
    const second = { refresh: vi.fn(), toggleHiddenFiles: vi.fn() }

    unregisters.push(registerDialogFileTreeOps(first))
    unregisters.push(registerDialogFileTreeOps(second))

    refreshFileTree()
    toggleHiddenFiles()

    expect(first.refresh).not.toHaveBeenCalled()
    expect(first.toggleHiddenFiles).not.toHaveBeenCalled()
    expect(second.refresh).toHaveBeenCalledOnce()
    expect(second.toggleHiddenFiles).toHaveBeenCalledOnce()
  })

  it('falls back to the last registered sidebar ops when dialogs unregister', () => {
    const sidebar = { refresh: vi.fn(), toggleHiddenFiles: vi.fn() }
    const dialog = { refresh: vi.fn(), toggleHiddenFiles: vi.fn() }

    const unregisterSidebar = registerSidebarFileTreeOps(sidebar)
    const unregisterDialog = registerDialogFileTreeOps(dialog)
    unregisters.push(unregisterSidebar, unregisterDialog)

    unregisterDialog()

    refreshFileTree()
    toggleHiddenFiles()

    expect(dialog.refresh).not.toHaveBeenCalled()
    expect(dialog.toggleHiddenFiles).not.toHaveBeenCalled()
    expect(sidebar.refresh).toHaveBeenCalledOnce()
    expect(sidebar.toggleHiddenFiles).toHaveBeenCalledOnce()
  })

  it('dispatches to the last registered sidebar when no dialog is active', () => {
    const first = { refresh: vi.fn(), toggleHiddenFiles: vi.fn() }
    const second = { refresh: vi.fn(), toggleHiddenFiles: vi.fn() }

    unregisters.push(registerSidebarFileTreeOps(first))
    unregisters.push(registerSidebarFileTreeOps(second))

    refreshFileTree()
    toggleHiddenFiles()

    expect(first.refresh).not.toHaveBeenCalled()
    expect(first.toggleHiddenFiles).not.toHaveBeenCalled()
    expect(second.refresh).toHaveBeenCalledOnce()
    expect(second.toggleHiddenFiles).toHaveBeenCalledOnce()
  })

  it('unregister only removes the owning registration', () => {
    const first = { refresh: vi.fn(), toggleHiddenFiles: vi.fn() }
    const second = { refresh: vi.fn(), toggleHiddenFiles: vi.fn() }

    const unregisterFirst = registerSidebarFileTreeOps(first)
    unregisters.push(unregisterFirst)
    unregisters.push(registerSidebarFileTreeOps(second))

    unregisterFirst()

    refreshFileTree()

    expect(first.refresh).not.toHaveBeenCalled()
    expect(second.refresh).toHaveBeenCalledOnce()
  })

  it('is a no-op when nothing is registered', () => {
    expect(() => {
      refreshFileTree()
      toggleHiddenFiles()
    }).not.toThrow()
  })
})
