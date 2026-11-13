import type { ActiveOp, FileSaveActions } from '~/components/common/fileSaveActions'
import type { SaveActionsSpies } from '~/test-support/saveActionsMocks'
import { fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import {
  fileDownloadMock,
  platformBridgeMock,
  preferencesMock,
  resetSaveActionsSpies,
  toastMock,
} from '~/test-support/saveActionsMocks'

const spies = vi.hoisted<SaveActionsSpies>(() => ({
  downloadImpl: vi.fn(),
  saveAsImpl: vi.fn(),
  saveToDownloadsImpl: vi.fn(),
  revealImpl: vi.fn(),
  warnToastImpl: vi.fn(),
  infoToastImpl: vi.fn(),
  revealAfterDownloadImpl: vi.fn(() => true),
  isTauriAppImpl: vi.fn(() => false),
}))

/** How many times the menu created its own save actions. */
const saveActionsCreated = vi.hoisted(() => ({ count: 0 }))

vi.mock('~/api/platformBridge', () => platformBridgeMock(spies))
vi.mock('~/lib/fileDownload', () => fileDownloadMock(spies))
vi.mock('~/components/common/Toast', () => toastMock(spies))
vi.mock('~/context/PreferencesContext', () => preferencesMock(spies))
vi.mock('~/components/common/fileSaveActions', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/components/common/fileSaveActions')>()
  return {
    ...actual,
    createFileSaveActions: (...args: Parameters<typeof actual.createFileSaveActions>) => {
      saveActionsCreated.count++
      return actual.createFileSaveActions(...args)
    },
  }
})

const { FileActionsMenu } = await import('~/components/common/FileActionsMenu')

function renderMenu(overrides: Partial<Omit<Parameters<typeof FileActionsMenu>[0], 'rootPath'>> & { rootPath?: string | undefined } = {}) {
  const { rootPath, ...rest } = overrides
  // Use a single merged object so an override replaces its default. `rootPath`
  // is merged by the `in` test rather than spread: the test that supplies
  // `rootPath: undefined` means "not supplied" (the component reads the prop
  // by value, so a dropped key and a written `undefined` reach it the same),
  // and the component's own `rootPath?: string` refuses an explicit
  // `undefined` under exactOptionalPropertyTypes.
  const props: Parameters<typeof FileActionsMenu>[0] = {
    workerId: 'w1',
    path: '/repo/src/file.ts',
    flavor: 'posix',
    homeDir: '/home/alice',
    ...rest,
  }
  if (!('rootPath' in overrides))
    props.rootPath = '/repo'
  else if (rootPath !== undefined)
    props.rootPath = rootPath
  return render(() => <FileActionsMenu {...props} />)
}

beforeEach(() => {
  resetSaveActionsSpies(spies)
  saveActionsCreated.count = 0
})

/** The popover of the one menu that the test rendered. */
function menuPopover(): HTMLElement {
  const popover = document.querySelector<HTMLElement>('[popover]')
  if (!popover)
    throw new Error('expected the menu to render its popover')
  return popover
}

/** The items that the menu mounts: the info block and every action. */
function menuItems(): HTMLElement[] {
  return [
    ...menuPopover().querySelectorAll<HTMLElement>('[role="menuitem"]'),
  ]
}

/**
 * Open the menu through its trigger. The items mount at the first open, so a
 * test of an item opens the menu first.
 *
 * Every variant of the menu holds "Copy path", so the check below fails a menu
 * that did not open. A test that asserts an absent item cannot pass on such a
 * menu.
 */
function openMenu(): void {
  fireEvent.click(screen.getByTestId('file-actions-trigger'))
  expect(menuPopover().matches(':popover-open')).toBe(true)
  expect(menuItems()).not.toEqual([])
}

/**
 * Every directory tree row holds one of these menus, and the user opens few of
 * them. Each item costs an icon, a button and its bindings, so a closed menu
 * mounts none until its first open.
 */
describe('FileActionsMenu — item mount', () => {
  it('mounts no item while the menu has never opened', () => {
    renderMenu({
      onMention: vi.fn(),
      size: 2048,
      modTime: '2026-05-01T10:00:00Z',
    })
    expect(menuItems()).toEqual([])
  })

  it('mounts the items before the popover shows', () => {
    renderMenu({
      onMention: vi.fn(),
    })
    const popover = menuPopover()
    let itemsAtBeforeToggle: string[] = []
    let openAtBeforeToggle: boolean | undefined
    // Added after the menu's own listener, so it runs after the menu handled
    // the same event.
    popover.addEventListener('beforetoggle', () => {
      itemsAtBeforeToggle = menuItems().map(item => item.dataset.testid ?? '')
      openAtBeforeToggle = popover.matches(':popover-open')
    }, { once: true })

    openMenu()

    expect(openAtBeforeToggle).toBe(false)
    expect(itemsAtBeforeToggle).toEqual([
      'file-actions-mention-button',
      'file-actions-copy-path-button',
      'file-actions-copy-relative-path-button',
      'file-actions-download-button',
    ])
  })

  // The info rows mount from the open state. A state that only `toggle` sets
  // arrives after the popover shows, so the popover paints one frame without the
  // block and then grows.
  it('mounts the info block before the popover shows', () => {
    renderMenu({ size: 2048, modTime: '2026-05-01T10:00:00Z' })
    const popover = menuPopover()
    let infoAtBeforeToggle: boolean | undefined
    // Added after the menu's own listener, so it runs after the menu handled
    // the same event.
    popover.addEventListener('beforetoggle', () => {
      infoAtBeforeToggle = popover.querySelector('[data-testid="file-actions-info-button"]') !== null
    }, { once: true })

    openMenu()

    expect(infoAtBeforeToggle).toBe(true)
  })

  it('keeps the action items after the menu closes, and drops the info block', () => {
    renderMenu({ size: 2048, modTime: '2026-05-01T10:00:00Z' })
    openMenu()
    expect(screen.getByTestId('file-actions-info-button')).toBeInTheDocument()

    menuPopover().hidePopover()

    expect(menuPopover().matches(':popover-open')).toBe(false)
    expect(screen.getByTestId('file-actions-copy-path-button')).toBeInTheDocument()
    // The info block holds a live `RelativeTimeAgo`, so a closed menu drops it.
    expect(screen.queryByTestId('file-actions-info-button')).not.toBeInTheDocument()
  })

  // The items stay mounted after the first open, so only the open state brings
  // the info block back, and it must do so before the reopened popover shows.
  it('builds the info block again before the popover shows on a reopen', () => {
    renderMenu({ size: 2048, modTime: '2026-05-01T10:00:00Z' })
    openMenu()
    menuPopover().hidePopover()
    const popover = menuPopover()
    let infoAtBeforeToggle: boolean | undefined
    // Added after the menu's own listener, so it runs after the menu handled
    // the same event.
    popover.addEventListener('beforetoggle', () => {
      infoAtBeforeToggle = popover.querySelector('[data-testid="file-actions-info-button"]') !== null
    }, { once: true })

    openMenu()

    expect(infoAtBeforeToggle).toBe(true)
  })

  // A right-click on the row opens the same menu without its trigger, and the
  // items must mount for that open as well.
  it('mounts the items when a right-click on its row opens the menu', () => {
    vi.useFakeTimers()
    try {
      let row!: HTMLDivElement
      render(() => (
        <>
          <div ref={row}>row</div>
          <FileActionsMenu workerId="w1" path="/repo/src/file.ts" flavor="posix" contextMenuFor={() => row} />
        </>
      ))
      expect(menuItems()).toEqual([])

      row.dispatchEvent(new MouseEvent('contextmenu', {
        clientX: 10,
        clientY: 10,
        bubbles: true,
        cancelable: true,
      }))
      vi.runAllTimers()

      expect(menuPopover().matches(':popover-open')).toBe(true)
      expect(menuItems().map(item => item.dataset.testid)).toEqual([
        'file-actions-copy-path-button',
        'file-actions-download-button',
      ])
    }
    finally {
      vi.useRealTimers()
    }
  })
})

/**
 * A menu creates its own save actions only for the items that use them, and
 * only once those items mount: a row the user never opens pays for none.
 */
describe('FileActionsMenu — save actions', () => {
  it('creates the save actions of a file menu at its first open, and only once', () => {
    renderMenu()
    expect(saveActionsCreated.count).toBe(0)

    openMenu()
    expect(saveActionsCreated.count).toBe(1)

    menuPopover().hidePopover()
    openMenu()
    expect(saveActionsCreated.count).toBe(1)
  })

  it('creates no save actions for a directory, which offers no save item', () => {
    renderMenu({ isDir: true })
    openMenu()
    expect(saveActionsCreated.count).toBe(0)
  })

  it('reads the busy state of injected save actions, and creates none', () => {
    const [op, setOp] = createSignal<ActiveOp | null>({ kind: 'download', progress: null })
    const actions: FileSaveActions = {
      op,
      handleDownload: vi.fn(),
      handleSaveAs: vi.fn(),
      handleSaveToDownloads: vi.fn(),
    }
    renderMenu({ actions })
    openMenu()

    expect(screen.getByTestId('file-actions-download-button')).toBeDisabled()
    setOp(null)
    expect(screen.getByTestId('file-actions-download-button')).toBeEnabled()
    expect(saveActionsCreated.count).toBe(0)
  })

  /**
   * The popover shows before `toggle` fires, so a busy state that waits for
   * `toggle` paints one frame of an enabled item while its save still runs.
   */
  it('shows a running download as disabled from the first frame of a reopen', async () => {
    let finishDownload!: () => void
    spies.downloadImpl.mockImplementation(() => new Promise<void>((resolve) => {
      finishDownload = resolve
    }))
    renderMenu()
    openMenu()
    fireEvent.click(screen.getByTestId('file-actions-download-button'))
    // A click on an item closes the menu.
    expect(menuPopover().matches(':popover-open')).toBe(false)

    const popover = menuPopover()
    let disabledAtBeforeToggle: boolean | undefined
    // Added after the menu's own listener, so it runs after the menu handled
    // the same event.
    popover.addEventListener('beforetoggle', () => {
      disabledAtBeforeToggle = screen.getByTestId<HTMLButtonElement>('file-actions-download-button').disabled
    }, { once: true })
    openMenu()
    expect(disabledAtBeforeToggle).toBe(true)

    finishDownload()
    await waitFor(() => expect(screen.getByTestId('file-actions-download-button')).toBeEnabled())
  })
})

describe('FileActionsMenu — common items', () => {
  it('always shows Copy path; shows Copy relative path when rootPath is supplied', () => {
    renderMenu()
    openMenu()
    expect(screen.getByTestId('file-actions-copy-path-button')).toBeInTheDocument()
    expect(screen.getByTestId('file-actions-copy-relative-path-button')).toBeInTheDocument()
  })

  it('hides Copy relative path when rootPath is not supplied', () => {
    renderMenu({ rootPath: undefined })
    openMenu()
    expect(screen.queryByTestId('file-actions-copy-relative-path-button')).not.toBeInTheDocument()
  })

  /**
   * Every absolute path is under a filesystem root, so `relativizePath`
   * refuses one as a base and answers the absolute path -- byte for byte what
   * "Copy path" copies. The directory picker roots its tree at `/` (or a
   * Windows drive), so without this the menu offers two items that do the
   * same thing for every path outside the home directory.
   */
  it('hides Copy relative path when the root IS the filesystem root', () => {
    renderMenu({ rootPath: '/', path: '/etc/hosts' })
    openMenu()
    expect(screen.getByTestId('file-actions-copy-path-button')).toBeInTheDocument()
    expect(screen.queryByTestId('file-actions-copy-relative-path-button')).not.toBeInTheDocument()
  })

  it('hides Copy relative path for a win32 drive root in either spelling', () => {
    const a = renderMenu({ rootPath: 'C:\\', path: 'C:\\Users\\alice\\a.ts', flavor: 'win32' })
    openMenu()
    expect(screen.queryByTestId('file-actions-copy-relative-path-button')).not.toBeInTheDocument()
    a.unmount()

    renderMenu({ rootPath: 'C:/', path: 'C:\\Users\\alice\\a.ts', flavor: 'win32' })
    openMenu()
    expect(screen.queryByTestId('file-actions-copy-relative-path-button')).not.toBeInTheDocument()
  })

  // A real directory base still offers it: the item is only useless for a root.
  it('keeps Copy relative path for an ordinary directory root', () => {
    renderMenu({ rootPath: '/repo', path: '/repo/src/main.ts' })
    openMenu()
    expect(screen.getByTestId('file-actions-copy-relative-path-button')).toBeInTheDocument()
  })

  it('shows Mention only when onMention is supplied', () => {
    const { unmount } = renderMenu()
    openMenu()
    expect(screen.queryByTestId('file-actions-mention-button')).not.toBeInTheDocument()
    unmount()

    const onMention = vi.fn()
    renderMenu({ onMention })
    openMenu()
    fireEvent.click(screen.getByTestId('file-actions-mention-button'))
    expect(onMention).toHaveBeenCalledWith('/repo/src/file.ts')
  })

  it('shows Open terminal only for directories (and with handler)', () => {
    const onOpenTerminal = vi.fn()
    const { unmount } = renderMenu({ isDir: false, onOpenTerminal })
    openMenu()
    expect(screen.queryByTestId('file-actions-open-terminal-button')).not.toBeInTheDocument()
    unmount()

    renderMenu({ isDir: true, onOpenTerminal })
    openMenu()
    fireEvent.click(screen.getByTestId('file-actions-open-terminal-button'))
    expect(onOpenTerminal).toHaveBeenCalledWith('/repo/src/file.ts')
  })

  it('writes the path to the clipboard on Copy path click', () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.assign(navigator, {
      clipboard: { writeText },
    })
    renderMenu()
    openMenu()
    fireEvent.click(screen.getByTestId('file-actions-copy-path-button'))
    expect(writeText).toHaveBeenCalledWith('/repo/src/file.ts')
  })

  it('writes the relative path on Copy relative path click', () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.assign(navigator, {
      clipboard: { writeText },
    })
    renderMenu()
    openMenu()
    fireEvent.click(screen.getByTestId('file-actions-copy-relative-path-button'))
    expect(writeText).toHaveBeenCalledWith('src/file.ts')
  })

  it('hides the Download / Save items for directories', () => {
    renderMenu({ isDir: true })
    openMenu()
    expect(screen.queryByTestId('file-actions-download-button')).not.toBeInTheDocument()
    expect(screen.queryByTestId('file-actions-save-as-button')).not.toBeInTheDocument()
    expect(screen.queryByTestId('file-actions-save-to-downloads-button')).not.toBeInTheDocument()
  })
})

describe('FileActionsMenu — web mode', () => {
  it('shows a single Download item that calls downloadFileFromWorker', async () => {
    renderMenu()
    openMenu()
    const dl = screen.getByTestId('file-actions-download-button')
    expect(dl).toBeInTheDocument()
    expect(screen.queryByTestId('file-actions-save-as-button')).not.toBeInTheDocument()
    expect(screen.queryByTestId('file-actions-save-to-downloads-button')).not.toBeInTheDocument()
    fireEvent.click(dl)
    await waitFor(() => expect(spies.downloadImpl).toHaveBeenCalledWith('w1', '/repo/src/file.ts', 'posix', expect.any(Function)))
  })
})

describe('FileActionsMenu — desktop mode', () => {
  beforeEach(() => {
    spies.isTauriAppImpl.mockReturnValue(true)
  })

  it('shows Save as... and Save to Downloads, hides web Download', () => {
    renderMenu()
    openMenu()
    expect(screen.getByTestId('file-actions-save-as-button')).toBeInTheDocument()
    expect(screen.getByTestId('file-actions-save-to-downloads-button')).toBeInTheDocument()
    expect(screen.queryByTestId('file-actions-download-button')).not.toBeInTheDocument()
  })

  it('save to Downloads reveals when the preference is on', async () => {
    renderMenu()
    openMenu()
    fireEvent.click(screen.getByTestId('file-actions-save-to-downloads-button'))
    await waitFor(() => expect(spies.saveToDownloadsImpl).toHaveBeenCalledWith('w1', '/repo/src/file.ts', 'posix', expect.any(Function)))
    await waitFor(() => expect(spies.revealImpl).toHaveBeenCalledWith('/home/alice/Downloads/file.ts'))
  })

  it('save as → skips reveal when the user cancels the dialog', async () => {
    spies.saveAsImpl.mockResolvedValueOnce(null)
    renderMenu()
    openMenu()
    fireEvent.click(screen.getByTestId('file-actions-save-as-button'))
    await waitFor(() => expect(spies.saveAsImpl).toHaveBeenCalledTimes(1))
    expect(spies.revealImpl).not.toHaveBeenCalled()
  })

  it('save to Downloads does NOT reveal when the preference is off', async () => {
    spies.revealAfterDownloadImpl.mockReturnValue(false)
    renderMenu()
    openMenu()
    fireEvent.click(screen.getByTestId('file-actions-save-to-downloads-button'))
    await waitFor(() => expect(spies.saveToDownloadsImpl).toHaveBeenCalledTimes(1))
    expect(spies.revealImpl).not.toHaveBeenCalled()
  })

  it('disables both save items while a save runs, and enables them after it ends', () => {
    const [op, setOp] = createSignal<ActiveOp | null>({ kind: 'save-as', progress: null })
    renderMenu({
      actions: {
        op,
        handleDownload: vi.fn(),
        handleSaveAs: vi.fn(),
        handleSaveToDownloads: vi.fn(),
      },
    })
    openMenu()

    expect(screen.getByTestId('file-actions-save-as-button')).toBeDisabled()
    expect(screen.getByTestId('file-actions-save-to-downloads-button')).toBeDisabled()
    setOp(null)
    expect(screen.getByTestId('file-actions-save-as-button')).toBeEnabled()
    expect(screen.getByTestId('file-actions-save-to-downloads-button')).toBeEnabled()
  })

  it('respects a custom itemTestIdPrefix', () => {
    renderMenu({ itemTestIdPrefix: 'tree' })
    openMenu()
    expect(screen.getByTestId('tree-save-as-button')).toBeInTheDocument()
    expect(screen.getByTestId('tree-save-to-downloads-button')).toBeInTheDocument()
    expect(screen.queryByTestId('file-actions-save-as-button')).not.toBeInTheDocument()
  })
})

describe('FileActionsMenu — size and modified info', () => {
  const modTime = '2026-05-01T10:00:00Z'

  // The block is built only while the popover is open. The builder is a memo,
  // which runs at setup outside the items that mount at the first open, so an
  // ungated builder mounted a live `RelativeTime` and a shared-tick subscriber
  // for every menu -- one per tree row. Opening first is therefore part of
  // what these tests assert, not harness noise.

  it('builds nothing until the menu opens', () => {
    renderMenu({ size: 2048, modTime })
    expect(screen.queryByTestId('file-actions-info-button')).not.toBeInTheDocument()
    openMenu()
    expect(screen.getByTestId('file-actions-info-button')).toBeInTheDocument()
  })

  it('is absent when neither size nor modTime is supplied', () => {
    renderMenu()
    openMenu()
    expect(screen.queryByTestId('file-actions-info-button')).not.toBeInTheDocument()
  })

  it('shows both size and modified for a file', () => {
    renderMenu({ size: 2048, modTime })
    openMenu()
    const info = screen.getByTestId('file-actions-info-button')
    expect(info.textContent).toContain('Size:')
    expect(info.textContent).toContain('2.0 KB')
    expect(info.textContent).toContain('Modified:')
    expect(info.textContent).toContain('ago')
  })

  it('shows a zero-byte file as 0 B rather than hiding the row', () => {
    renderMenu({ size: 0, modTime })
    openMenu()
    expect(screen.getByTestId('file-actions-info-button').textContent).toContain('0 B')
  })

  it('omits the size for a directory but keeps the modified time', () => {
    renderMenu({ isDir: true, size: 4096, modTime })
    openMenu()
    const info = screen.getByTestId('file-actions-info-button')
    expect(info.textContent).not.toContain('Size:')
    expect(info.textContent).toContain('Modified:')
  })

  it('renders nothing for a directory with no modified time', () => {
    renderMenu({ isDir: true, size: 4096 })
    openMenu()
    expect(screen.queryByTestId('file-actions-info-button')).not.toBeInTheDocument()
  })

  it('copies the path, size and modified time, and toasts', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.assign(navigator, {
      clipboard: { writeText },
    })
    renderMenu({ size: 2048, modTime })
    openMenu()

    fireEvent.click(screen.getByTestId('file-actions-info-button'))

    await waitFor(() => expect(writeText).toHaveBeenCalled())
    const copied = writeText.mock.calls[0]?.[0]
    if (copied === undefined)
      throw new Error('expected the info copy to have run')
    expect(JSON.parse(copied)).toEqual({
      path: '/repo/src/file.ts',
      size: 2048,
      modified: modTime,
    })
    await waitFor(() => expect(spies.infoToastImpl).toHaveBeenCalledWith('File info copied to clipboard'))
  })

  it('leaves the size out of the copied text for a directory', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.assign(navigator, {
      clipboard: { writeText },
    })
    renderMenu({
      isDir: true,
      size: 4096,
      modTime,
      path: '/repo/src',
    })
    openMenu()

    fireEvent.click(screen.getByTestId('file-actions-info-button'))

    await waitFor(() => expect(writeText).toHaveBeenCalled())
    const copied = writeText.mock.calls[0]?.[0]
    if (copied === undefined)
      throw new Error('expected the info copy to have run')
    expect(JSON.parse(copied)).toEqual({
      path: '/repo/src',
      modified: modTime,
    })
    await waitFor(() => expect(spies.infoToastImpl).toHaveBeenCalledWith('Directory info copied to clipboard'))
  })
})
