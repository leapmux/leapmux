import { fireEvent, render, screen } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { UnsupportedFileView } from '~/components/fileviewer/UnsupportedFileView'

function noop() {}

function renderView(overrides: Partial<Parameters<typeof UnsupportedFileView>[0]> = {}) {
  const props = {
    filePath: '/repo/archive.zip',
    flavor: 'posix' as const,
    totalSize: 2048,
    reason: 'binary' as const,
    op: null,
    loadingAnyway: false,
    canShowAnyway: true,
    onDownload: noop,
    onShowAnyway: noop,
    ...overrides,
  }
  return render(() => <UnsupportedFileView {...props} />)
}

describe('unsupportedFileView', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
  })

  it('renders title, filename·size body, and both action buttons', () => {
    renderView()
    expect(screen.getByText('This file cannot be displayed inline.')).toBeInTheDocument()
    const meta = screen.getByTestId('unsupported-meta')
    expect(meta.textContent).toContain('archive.zip')
    expect(meta.textContent).toMatch(/2(\.0)?\s*KB/i)
    expect(screen.getByTestId('unsupported-download-button')).toBeInTheDocument()
    expect(screen.getByTestId('unsupported-show-anyway-button')).toBeInTheDocument()
  })

  it('renders the binary reason title', () => {
    renderView({ reason: 'binary' })
    expect(screen.getByText('This file cannot be displayed inline.')).toBeInTheDocument()
  })

  it('renders the oversize-image reason title', () => {
    renderView({ reason: 'oversize-image', filePath: '/repo/huge.png', totalSize: 5_000_000 })
    expect(screen.getByText('This image is too large to preview.')).toBeInTheDocument()
  })

  it('fires onDownload when Download is clicked', () => {
    const onDownload = vi.fn()
    renderView({ onDownload })
    fireEvent.click(screen.getByTestId('unsupported-download-button'))
    expect(onDownload).toHaveBeenCalledTimes(1)
  })

  it('fires onShowAnyway when Show anyway is clicked', () => {
    const onShowAnyway = vi.fn()
    renderView({ onShowAnyway })
    fireEvent.click(screen.getByTestId('unsupported-show-anyway-button'))
    expect(onShowAnyway).toHaveBeenCalledTimes(1)
  })

  it('omits the Show anyway button when canShowAnyway is false', () => {
    renderView({ canShowAnyway: false })
    expect(screen.queryByTestId('unsupported-show-anyway-button')).not.toBeInTheDocument()
    // Download still rendered.
    expect(screen.getByTestId('unsupported-download-button')).toBeInTheDocument()
  })

  it('disables Download and shows a spinner while op.kind is "download"', () => {
    renderView({ op: { kind: 'download', progress: null } })
    const btn = screen.getByTestId('unsupported-download-button') as HTMLButtonElement
    expect(btn.disabled).toBe(true)
    expect(btn.textContent).toMatch(/Downloading/i)
  })

  it('appends a percent to the busy label when op.progress is non-null', () => {
    renderView({ op: { kind: 'download', progress: 45 } })
    const btn = screen.getByTestId('unsupported-download-button') as HTMLButtonElement
    expect(btn.textContent).toMatch(/Downloading\.\.\. 45%/)
  })

  it('omits the percent when op.progress is null', () => {
    renderView({ op: { kind: 'download', progress: null } })
    const btn = screen.getByTestId('unsupported-download-button') as HTMLButtonElement
    expect(btn.textContent).toMatch(/Downloading\.\.\./)
    expect(btn.textContent).not.toMatch(/%/)
  })

  it('disables Show anyway and shows a spinner while loadingAnyway is true', () => {
    renderView({ loadingAnyway: true })
    const btn = screen.getByTestId('unsupported-show-anyway-button') as HTMLButtonElement
    expect(btn.disabled).toBe(true)
    expect(btn.textContent).toMatch(/Loading/i)
  })

  it('attaches ARIA labels with the filename', () => {
    renderView({ filePath: 'C:\\users\\trustin\\photo.png', flavor: 'win32', reason: 'oversize-image' })
    expect(screen.getByLabelText('Download photo.png')).toBeInTheDocument()
    expect(screen.getByLabelText('Show photo.png anyway')).toBeInTheDocument()
  })

  it('marks the wrapper as a region labelled by the title heading', () => {
    renderView()
    const region = screen.getByTestId('unsupported-file-view')
    expect(region).toHaveAttribute('role', 'region')
    const labelId = region.getAttribute('aria-labelledby')
    expect(labelId).toBeTruthy()
    const heading = document.getElementById(labelId!)
    expect(heading).not.toBeNull()
    expect(heading!.tagName).toBe('H2')
    expect(heading!.textContent).toBe('This file cannot be displayed inline.')
  })

  it('does not autofocus any button on mount', () => {
    renderView()
    expect(document.activeElement).toBe(document.body)
  })

  describe('desktop mode', () => {
    function renderDesktopView(
      overrides: Partial<Parameters<typeof UnsupportedFileView>[0]>
        & Partial<NonNullable<Parameters<typeof UnsupportedFileView>[0]['desktop']>> = {},
    ) {
      const onSaveAs = vi.fn()
      const onSaveToDownloads = vi.fn()
      const onRevealChange = vi.fn()
      const {
        revealAfterDownload,
        onSaveAs: onSaveAsOverride,
        onSaveToDownloads: onSaveToDownloadsOverride,
        onRevealAfterDownloadChange: onRevealChangeOverride,
        ...topLevel
      } = overrides
      renderView({
        ...topLevel,
        desktop: {
          onSaveAs: onSaveAsOverride ?? onSaveAs,
          onSaveToDownloads: onSaveToDownloadsOverride ?? onSaveToDownloads,
          revealAfterDownload: revealAfterDownload ?? false,
          onRevealAfterDownloadChange: onRevealChangeOverride ?? onRevealChange,
        },
      })
      return { onSaveAs, onSaveToDownloads, onRevealChange }
    }

    it('renders Save as / Save to Downloads instead of Download', () => {
      renderDesktopView()
      expect(screen.queryByTestId('unsupported-download-button')).not.toBeInTheDocument()
      expect(screen.getByTestId('unsupported-save-as-button')).toBeInTheDocument()
      expect(screen.getByTestId('unsupported-save-to-downloads-button')).toBeInTheDocument()
    })

    it('fires onSaveAs / onSaveToDownloads when the respective buttons are clicked', () => {
      const { onSaveAs, onSaveToDownloads } = renderDesktopView()
      fireEvent.click(screen.getByTestId('unsupported-save-as-button'))
      expect(onSaveAs).toHaveBeenCalledTimes(1)
      expect(onSaveToDownloads).not.toHaveBeenCalled()

      fireEvent.click(screen.getByTestId('unsupported-save-to-downloads-button'))
      expect(onSaveToDownloads).toHaveBeenCalledTimes(1)
    })

    it('disables both save buttons while a save is in flight', () => {
      renderDesktopView({ op: { kind: 'save-as', progress: null } })
      const saveAs = screen.getByTestId('unsupported-save-as-button') as HTMLButtonElement
      const toDownloads = screen.getByTestId('unsupported-save-to-downloads-button') as HTMLButtonElement
      expect(saveAs.disabled).toBe(true)
      expect(toDownloads.disabled).toBe(true)
    })

    it('shows the spinner on the Save-as button when op.kind is "save-as"', () => {
      renderDesktopView({ op: { kind: 'save-as', progress: null } })
      const saveAs = screen.getByTestId('unsupported-save-as-button') as HTMLButtonElement
      const toDownloads = screen.getByTestId('unsupported-save-to-downloads-button') as HTMLButtonElement
      expect(saveAs.textContent).toMatch(/Saving/i)
      expect(toDownloads.textContent).not.toMatch(/Saving/i)
      expect(toDownloads.textContent).toMatch(/Save to Downloads/i)
    })

    it('shows percent progress alongside the spinner during a Save as...', () => {
      renderDesktopView({ op: { kind: 'save-as', progress: 25 } })
      const saveAs = screen.getByTestId('unsupported-save-as-button') as HTMLButtonElement
      expect(saveAs.textContent).toMatch(/Saving\.\.\. 25%/)
    })

    it('shows percent progress during a Save to Downloads', () => {
      renderDesktopView({ op: { kind: 'save-to-downloads', progress: 90 } })
      const toDownloads = screen.getByTestId('unsupported-save-to-downloads-button') as HTMLButtonElement
      expect(toDownloads.textContent).toMatch(/Saving\.\.\. 90%/)
    })

    it('shows the spinner on the Save-to-Downloads button when op.kind is "save-to-downloads"', () => {
      renderDesktopView({ op: { kind: 'save-to-downloads', progress: null } })
      const saveAs = screen.getByTestId('unsupported-save-as-button') as HTMLButtonElement
      const toDownloads = screen.getByTestId('unsupported-save-to-downloads-button') as HTMLButtonElement
      expect(toDownloads.textContent).toMatch(/Saving/i)
      expect(saveAs.textContent).not.toMatch(/Saving/i)
      expect(saveAs.textContent).toMatch(/Save as/i)
    })

    it('renders the reveal checkbox reflecting the preference', () => {
      renderDesktopView({ revealAfterDownload: true })
      const checkbox = screen.getByTestId('unsupported-reveal-checkbox') as HTMLInputElement
      expect(checkbox.checked).toBe(true)
    })

    it('fires onRevealAfterDownloadChange when the checkbox is toggled', () => {
      const { onRevealChange } = renderDesktopView({ revealAfterDownload: false })
      const checkbox = screen.getByTestId('unsupported-reveal-checkbox') as HTMLInputElement
      fireEvent.click(checkbox)
      expect(onRevealChange).toHaveBeenCalledWith(true)
    })

    it('omits the reveal checkbox in web mode (no desktop prop)', () => {
      renderView()
      expect(screen.queryByTestId('unsupported-reveal-checkbox')).not.toBeInTheDocument()
    })
  })
})
