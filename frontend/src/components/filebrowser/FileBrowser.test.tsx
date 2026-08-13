import type { FileInfo } from '~/generated/leapmux/v1/file_pb'
import { create } from '@bufbuild/protobuf'
import { render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { FileBrowser } from '~/components/filebrowser/FileBrowser'
import * as styles from '~/components/filebrowser/FileBrowser.css'
import { FileInfoSchema } from '~/generated/leapmux/v1/file_pb'
import { clippedText } from '~/styles/shared.css'
import { hoverForTooltip, stubClipped, stubFitting } from '~/test-support/clipStub'
import { classSelector } from '~/test-support/composedClass'

function makeEntry(name: string, isDir: boolean, size: bigint = 0n): FileInfo {
  return create(FileInfoSchema, {
    name,
    path: `/${name}`,
    isDir,
    size,
    modTime: '',
    permissions: '',
  })
}

describe('fileBrowser', () => {
  it('renders empty state', () => {
    render(() => (
      <FileBrowser
        currentPath="."
        entries={[]}
        loading={false}
        error={null}
        onNavigate={() => {}}
        onFileSelect={() => {}}
      />
    ))
    expect(screen.getByText('Empty directory')).toBeInTheDocument()
  })

  const LONG_NAME = 'a-file-name-far-wider-than-the-browser-column.tsx'

  function renderWithLongName() {
    return render(() => (
      <FileBrowser
        currentPath="."
        entries={[makeEntry(LONG_NAME, false, 10n)]}
        loading={false}
        error={null}
        onNavigate={() => {}}
        onFileSelect={() => {}}
      />
    ))
  }

  // The name clips to one line. It declared the ellipsis before but not the
  // `min-width: 0` a `flex: 1` item needs to shrink past its own text, so a long
  // name widened the row instead. `ClippedText` supplies both.
  it('clips a file name to one line', () => {
    const { container } = renderWithLongName()
    const name = container.querySelector(classSelector(styles.fileName))!
    expect(name.textContent).toBe(LONG_NAME)
    // Token membership, not a substring: a future class whose own name merely
    // CONTAINS "clippedText" would satisfy a regex and prove nothing.
    expect(name.className.trim().split(/\s+/)).toContain(clippedText)
  })

  // The clip alone would leave the rest of the name unreachable, which is the
  // pairing the shared component exists to enforce.
  describe('clipped name tooltip', () => {
    beforeEach(() => {
      vi.useFakeTimers()
    })

    afterEach(() => {
      vi.useRealTimers()
      vi.restoreAllMocks()
    })

    it('gives the full file name on hover once it is clipped', () => {
      const { container } = renderWithLongName()
      const name = container.querySelector<HTMLElement>(classSelector(styles.fileName))!
      stubClipped(name)
      expect(hoverForTooltip(name)?.textContent).toBe(LONG_NAME)
    })

    it('shows no tooltip while the file name fits', () => {
      const { container } = renderWithLongName()
      const name = container.querySelector<HTMLElement>(classSelector(styles.fileName))!
      stubFitting(name)
      expect(hoverForTooltip(name)).toBeNull()
    })
  })

  it('renders loading state', () => {
    render(() => (
      <FileBrowser
        currentPath="."
        entries={[]}
        loading
        error={null}
        onNavigate={() => {}}
        onFileSelect={() => {}}
      />
    ))
    expect(screen.getByText('Loading...')).toBeInTheDocument()
  })

  it('renders error state', () => {
    render(() => (
      <FileBrowser
        currentPath="."
        entries={[]}
        loading={false}
        error="Failed to load"
        onNavigate={() => {}}
        onFileSelect={() => {}}
      />
    ))
    expect(screen.getByText('Failed to load')).toBeInTheDocument()
  })

  it('renders file entries sorted (dirs first)', () => {
    const entries = [
      makeEntry('main.go', false, 512n),
      makeEntry('src', true),
      makeEntry('README.md', false, 256n),
    ]
    render(() => (
      <FileBrowser
        currentPath="/project"
        entries={entries}
        loading={false}
        error={null}
        onNavigate={() => {}}
        onFileSelect={() => {}}
      />
    ))
    expect(screen.getByText('src')).toBeInTheDocument()
    expect(screen.getByText('main.go')).toBeInTheDocument()
    expect(screen.getByText('README.md')).toBeInTheDocument()
  })

  it('calls onNavigate when directory is clicked', () => {
    const onNavigate = vi.fn()
    const entries = [makeEntry('src', true)]
    render(() => (
      <FileBrowser
        currentPath="."
        entries={entries}
        loading={false}
        error={null}
        onNavigate={onNavigate}
        onFileSelect={() => {}}
      />
    ))
    screen.getByText('src').click()
    expect(onNavigate).toHaveBeenCalledWith('/src')
  })

  it('calls onFileSelect when file is clicked', () => {
    const onFileSelect = vi.fn()
    const entries = [makeEntry('main.go', false)]
    render(() => (
      <FileBrowser
        currentPath="."
        entries={entries}
        loading={false}
        error={null}
        onNavigate={() => {}}
        onFileSelect={onFileSelect}
      />
    ))
    screen.getByText('main.go').click()
    expect(onFileSelect).toHaveBeenCalledWith(entries[0])
  })
})
