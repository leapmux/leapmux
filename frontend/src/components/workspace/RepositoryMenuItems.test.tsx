import type { RepositoryCheckout } from './RepositoryMenuItems'
import type { ExternalApp } from '~/api/platformBridge'
import type { ExternalApps } from '~/hooks/useExternalApps'
import { fireEvent, render, screen, within } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ExternalAppKind } from '~/generated/proto/leapmux/desktop/v1/frame_pb'
import { RepositoryMenuItems } from './RepositoryMenuItems'

const { copyTextMock, revealMock } = vi.hoisted(() => ({
  copyTextMock: vi.fn(),
  revealMock: vi.fn(),
}))

vi.mock('~/lib/clipboard', async importOriginal => ({
  ...await importOriginal<typeof import('~/lib/clipboard')>(),
  copyTextToClipboard: (...args: unknown[]) => copyTextMock(...args),
}))

vi.mock('~/api/platformBridge', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/api/platformBridge')>()
  return { ...actual, revealInFileManager: (...args: unknown[]) => revealMock(...args) }
})

function editor(id: string, displayName: string): ExternalApp {
  return { id, displayName, kind: ExternalAppKind.EDITOR }
}

const FINDER: ExternalApp = { id: 'file-manager', displayName: 'Finder', kind: ExternalAppKind.FILE_MANAGER }

const LOCAL: RepositoryCheckout = {
  gitToplevel: '/home/me/leapmux',
  originUrl: 'https://example.com/o/r.git',
  isLocal: true,
}

/**
 * A stand-in for the real hook. The hook owns the probe and the toast, which
 * are its own concern; what this component decides is WHICH rows exist and
 * what each one acts on.
 */
function stubApps(apps: ExternalApp[], preferred?: ExternalApp): ExternalApps {
  return {
    apps: () => apps,
    preferred: () => preferred,
    preferredId: () => preferred?.id,
    launch: vi.fn(),
    refresh: vi.fn(async () => {}),
    refreshing: () => false,
  }
}

function renderItems(checkout: RepositoryCheckout, apps: ExternalApps) {
  render(() => (
    <menu data-testid="host">
      <RepositoryMenuItems checkout={() => checkout} apps={apps} testIdPrefix="repo" />
    </menu>
  ))
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
})

describe('repositoryMenuItems', () => {
  it('offers every row for a local checkout with an origin', () => {
    renderItems(LOCAL, stubApps([editor('vscode', 'Visual Studio Code')], editor('vscode', 'Visual Studio Code')))

    expect(items()).toEqual([
      'Copy repository URL',
      'Copy repository path',
      'Reveal in file manager',
      'Open in Visual Studio Code',
      'Open in…',
    ])
  })

  it('carries its own section header, so it reads as one block wherever it lands', () => {
    renderItems(LOCAL, stubApps([]))

    expect(screen.getByText('Repository')).toBeInTheDocument()
  })

  it('drops Copy repository URL for a repository with no remote', () => {
    renderItems({ ...LOCAL, originUrl: '' }, stubApps([]))

    expect(items()).not.toContain('Copy repository URL')
    expect(items()).toContain('Copy repository path')
  })

  // Reveal and the two Open rows act on THIS machine, so a remote worker's
  // absolute path either does not exist here or -- worse -- exists and is a
  // different directory. The PATH is still worth copying: pasting it into an
  // ssh session on the machine that has it is exactly the use.
  it('hides every local-only row for a remote worker, and keeps the path', () => {
    renderItems({ ...LOCAL, isLocal: false }, stubApps([editor('vscode', 'VS Code')], editor('vscode', 'VS Code')))

    expect(items()).toEqual(['Copy repository URL', 'Copy repository path'])
  })

  it('copies the checkout path, not the origin URL', () => {
    renderItems(LOCAL, stubApps([]))

    fireEvent.click(screen.getByRole('menuitem', { name: 'Copy repository path', hidden: true }))
    expect(copyTextMock).toHaveBeenCalledWith('/home/me/leapmux')
  })

  it('copies the origin URL from its own row', () => {
    renderItems(LOCAL, stubApps([]))

    fireEvent.click(screen.getByRole('menuitem', { name: 'Copy repository URL', hidden: true }))
    expect(copyTextMock).toHaveBeenCalledWith('https://example.com/o/r.git')
  })

  it('reveals the checkout directory', () => {
    renderItems(LOCAL, stubApps([]))

    fireEvent.click(screen.getByRole('menuitem', { name: 'Reveal in file manager', hidden: true }))
    expect(revealMock).toHaveBeenCalledWith('/home/me/leapmux')
  })

  it('launches the remembered application at the checkout', () => {
    const apps = stubApps([editor('zed', 'Zed')], editor('zed', 'Zed'))
    renderItems(LOCAL, apps)

    fireEvent.click(screen.getByRole('menuitem', { name: 'Open in Zed', hidden: true }))
    expect(apps.launch).toHaveBeenCalledWith('zed', '/home/me/leapmux')
  })

  // "Open in Finder" directly under "Reveal in file manager" says almost the
  // same thing twice. The submenu still offers it, so nothing is unreachable.
  it('drops the Open in ... row when the remembered application is the file manager', () => {
    renderItems(LOCAL, stubApps([FINDER, editor('zed', 'Zed')], FINDER))

    expect(items()).toEqual([
      'Copy repository URL',
      'Copy repository path',
      'Reveal in file manager',
      'Open in…',
    ])
  })

  it('keeps the Open in ... row for an editor default', () => {
    renderItems(LOCAL, stubApps([FINDER, editor('zed', 'Zed')], editor('zed', 'Zed')))

    expect(items()).toContain('Open in Zed')
  })

  it('offers no Open in ... row when nothing is remembered yet', () => {
    renderItems(LOCAL, stubApps([editor('zed', 'Zed')]))

    expect(items()).toEqual([
      'Copy repository URL',
      'Copy repository path',
      'Reveal in file manager',
      'Open in…',
    ])
  })

  // A submenu that opens on an empty list is a dead end.
  it('hides the Open in ... submenu when no application was detected', () => {
    renderItems(LOCAL, stubApps([]))

    expect(items()).toEqual(['Copy repository URL', 'Copy repository path', 'Reveal in file manager'])
  })

  it('launches the application picked inside the submenu, at this checkout', () => {
    const apps = stubApps([FINDER, editor('zed', 'Zed')], editor('zed', 'Zed'))
    renderItems(LOCAL, apps)

    fireEvent.click(screen.getByTestId('repo-open-in'))
    fireEvent.click(screen.getByTestId('repo-item-file-manager'))
    expect(apps.launch).toHaveBeenCalledWith('file-manager', '/home/me/leapmux')
  })
})
