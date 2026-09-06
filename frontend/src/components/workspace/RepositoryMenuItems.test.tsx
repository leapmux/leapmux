import type { RepositoryCheckout } from './RepositoryMenuItems'
import type { ExternalApp } from '~/api/platformBridge'
import { fireEvent, render, screen, waitFor, within } from '@solidjs/testing-library'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { _resetExternalAppCacheForTests } from '~/lib/externalApps'
import { editorApp, fileManagerApp } from '~/test-support/externalAppFixtures'
import { RepositoryMenuItems } from './RepositoryMenuItems'

const { copyTextMock, revealMock, listAppsMock, openInExternalAppMock, prefs } = vi.hoisted(() => ({
  copyTextMock: vi.fn(),
  revealMock: vi.fn(),
  listAppsMock: vi.fn(),
  openInExternalAppMock: vi.fn(),
  prefs: {
    preferredExternalAppId: vi.fn<() => string | undefined>(),
    setPreferredExternalAppId: vi.fn<(id: string | undefined) => void>(),
  },
}))

vi.mock('~/lib/clipboard', async importOriginal => ({
  ...await importOriginal<typeof import('~/lib/clipboard')>(),
  copyTextToClipboard: (...args: unknown[]) => copyTextMock(...args),
}))

vi.mock('~/api/platformBridge', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/api/platformBridge')>()
  return {
    ...actual,
    revealInFileManager: (...args: unknown[]) => revealMock(...args),
    platformBridge: {
      ...actual.platformBridge,
      listExternalApps: (refresh?: boolean) => listAppsMock(refresh ?? false),
      openInExternalApp: (...args: unknown[]) => openInExternalAppMock(...args),
    },
  }
})

// The block stands its own probe up now, so it reads the pin through the
// context. Only the two members the hook touches need to behave, and both are
// spies so a test can assert the WRITE as well as the read.
vi.mock('~/context/PreferencesContext', async importOriginal => ({
  ...await importOriginal<typeof import('~/context/PreferencesContext')>(),
  usePreferences: () => prefs,
}))

const FINDER = fileManagerApp()

const LOCAL: RepositoryCheckout = {
  gitToplevel: '/home/me/leapmux',
  originUrl: 'https://example.com/o/r.git',
  isLocal: true,
}

/**
 * Render the block against a machine that reports `apps`, with `pinned`
 * remembered.
 *
 * Async because the block probes: the detected list arrives through a resource,
 * so a test that asserts a row naming an application has to wait for it. The
 * rows that do not depend on detection are there from the first paint.
 */
async function renderItems(
  checkout: RepositoryCheckout,
  apps: ExternalApp[] = [],
  pinned?: string,
) {
  listAppsMock.mockResolvedValue(apps)
  prefs.preferredExternalAppId.mockReturnValue(pinned)
  render(() => (
    <menu data-testid="host">
      <RepositoryMenuItems checkout={() => checkout} />
    </menu>
  ))
  // The probe runs only for a local checkout, and only it can add rows.
  if (checkout.isLocal && apps.length > 0)
    await waitFor(() => expect(items()).toContain('Open in…'))
}

/**
 * Every row label of the block, in order.
 *
 * `hidden: true` because the stubbed popover leaves the UA `display: none` on,
 * which takes the rows out of the accessibility tree. A closed submenu
 * contributes only its own trigger row: `SubMenu` mounts its children behind a
 * `<Show>`, so nothing inside it is here until it opens.
 */
function items(): string[] {
  return within(screen.getByTestId('host'))
    .queryAllByRole('menuitem', { hidden: true })
    .map(el => el.textContent?.trim() ?? '')
}

beforeEach(() => {
  copyTextMock.mockReset()
  revealMock.mockReset()
  listAppsMock.mockReset()
  listAppsMock.mockResolvedValue([])
  openInExternalAppMock.mockReset()
  openInExternalAppMock.mockResolvedValue(undefined)
  prefs.preferredExternalAppId.mockReset()
  prefs.setPreferredExternalAppId.mockReset()
  _resetExternalAppCacheForTests()
})

afterEach(() => {
  _resetExternalAppCacheForTests()
})

describe('repositoryMenuItems', () => {
  it('offers every row for a local checkout with an origin', async () => {
    await renderItems(LOCAL, [editorApp('vscode', 'Visual Studio Code')], 'vscode')

    expect(items()).toEqual([
      'Copy repository URL',
      'Copy repository path',
      'Reveal in file manager',
      'Open in Visual Studio Code',
      'Open in…',
    ])
  })

  it('carries its own section header, so it reads as one block wherever it lands', async () => {
    await renderItems(LOCAL)

    expect(screen.getByText('Repository')).toBeInTheDocument()
  })

  it('drops Copy repository URL for a repository with no remote', async () => {
    await renderItems({ ...LOCAL, originUrl: '' })

    expect(items()).not.toContain('Copy repository URL')
    expect(items()).toContain('Copy repository path')
  })

  // Reveal and the two Open rows act on THIS machine, so a remote worker's
  // absolute path either does not exist here or -- worse -- exists and is a
  // different directory. The PATH is still worth copying: pasting it into an
  // ssh session on the machine that has it is exactly the use.
  it('hides every local-only row for a remote worker, and keeps the path', async () => {
    await renderItems({ ...LOCAL, isLocal: false }, [editorApp('vscode', 'VS Code')], 'vscode')

    expect(items()).toEqual(['Copy repository URL', 'Copy repository path'])
    expect(listAppsMock).not.toHaveBeenCalled()
  })

  it('copies the checkout path, not the origin URL', async () => {
    await renderItems(LOCAL)

    fireEvent.click(screen.getByRole('menuitem', { name: 'Copy repository path', hidden: true }))
    expect(copyTextMock).toHaveBeenCalledWith('/home/me/leapmux')
  })

  it('copies the origin URL from its own row', async () => {
    await renderItems(LOCAL)

    fireEvent.click(screen.getByRole('menuitem', { name: 'Copy repository URL', hidden: true }))
    expect(copyTextMock).toHaveBeenCalledWith('https://example.com/o/r.git')
  })

  it('reveals the checkout directory', async () => {
    await renderItems(LOCAL)

    fireEvent.click(screen.getByRole('menuitem', { name: 'Reveal in file manager', hidden: true }))
    expect(revealMock).toHaveBeenCalledWith('/home/me/leapmux')
  })

  it('launches the remembered application at the checkout', async () => {
    await renderItems(LOCAL, [editorApp('zed', 'Zed')], 'zed')

    fireEvent.click(screen.getByRole('menuitem', { name: 'Open in Zed', hidden: true }))
    expect(openInExternalAppMock).toHaveBeenCalledWith('zed', '/home/me/leapmux')
  })

  // "Reveal in file manager" selects the directory inside its PARENT; this
  // opens the directory itself. Two different operations, so the row stays --
  // hiding it made an affordance vanish whenever a user picked Finder once.
  it('keeps the Open in ... row when the remembered application is the file manager', async () => {
    await renderItems(LOCAL, [FINDER, editorApp('zed', 'Zed')], 'file-manager')

    expect(items()).toEqual([
      'Copy repository URL',
      'Copy repository path',
      'Reveal in file manager',
      'Open in Finder',
      'Open in…',
    ])
  })

  it('keeps the Open in ... row for an editor default', async () => {
    await renderItems(LOCAL, [FINDER, editorApp('zed', 'Zed')], 'zed')

    expect(items()).toContain('Open in Zed')
  })

  // The row names an explicit choice, never a guess. The submenu below is one
  // hover away, and picking from it is what makes the choice.
  it('offers no Open in ... row when nothing is remembered yet', async () => {
    await renderItems(LOCAL, [editorApp('zed', 'Zed')])

    expect(items()).toEqual([
      'Copy repository URL',
      'Copy repository path',
      'Reveal in file manager',
      'Open in…',
    ])
  })

  // A submenu that opens on an empty list is a dead end.
  it('hides the Open in ... submenu when no application was detected', async () => {
    await renderItems(LOCAL)

    expect(items()).toEqual(['Copy repository URL', 'Copy repository path', 'Reveal in file manager'])
  })

  it('launches the application picked inside the submenu, at this checkout', async () => {
    await renderItems(LOCAL, [FINDER, editorApp('zed', 'Zed')], 'zed')

    fireEvent.click(screen.getByTestId('repository-open-in'))
    fireEvent.click(screen.getByTestId('repository-item-file-manager'))

    expect(openInExternalAppMock).toHaveBeenCalledWith('file-manager', '/home/me/leapmux')
    // Picking also REMEMBERS, on every surface that renders the list.
    expect(prefs.setPreferredExternalAppId).toHaveBeenCalledWith('file-manager')
  })
})
