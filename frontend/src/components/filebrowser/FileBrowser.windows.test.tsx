import type { FileInfo } from '~/generated/leapmux/v1/file_pb'
import { create } from '@bufbuild/protobuf'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { FileInfoSchema } from '~/generated/leapmux/v1/file_pb'
import { FileBrowser } from './FileBrowser'

function mkEntry(path: string, name: string, isDir: boolean): FileInfo {
  return create(FileInfoSchema, {
    name,
    path,
    isDir,
    size: 0n,
    modTime: '',
    permissions: '',
    hidden: false,
  })
}

describe('fileBrowser breadcrumbs with Windows paths', () => {
  it('splits a drive-letter path into volume + per-segment buttons', () => {
    const onNavigate = vi.fn()
    render(() => (
      <FileBrowser
        currentPath="C:\\Users\\alice\\proj"
        entries={[mkEntry('C:\\Users\\alice\\proj\\src', 'src', true)]}
        loading={false}
        error={null}
        onNavigate={onNavigate}
        onFileSelect={vi.fn()}
      />
    ))

    // Segment buttons are the path components: C:\, Users, alice, proj.
    // (Plus the leading "~" button.)
    expect(screen.getByRole('button', { name: 'C:\\' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Users' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'alice' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'proj' })).toBeInTheDocument()
  })

  it('renders a backslash separator between Windows segments', () => {
    render(() => (
      <FileBrowser
        currentPath="C:\\Users\\alice"
        entries={[]}
        loading={false}
        error={null}
        onNavigate={vi.fn()}
        onFileSelect={vi.fn()}
      />
    ))
    const breadcrumb = screen.getByRole('navigation', { name: /breadcrumb/i })
    expect(breadcrumb.textContent).toContain('\\')
  })

  it('navigates to the parent via the up-directory entry using Windows separators', () => {
    const onNavigate = vi.fn()
    render(() => (
      <FileBrowser
        currentPath="C:\\Users\\alice\\proj"
        entries={[mkEntry('C:\\Users\\alice\\proj\\x', 'x', true)]}
        loading={false}
        error={null}
        onNavigate={onNavigate}
        onFileSelect={vi.fn()}
      />
    ))
    const parentRow = screen.getAllByText('..')[0]
    fireEvent.click(parentRow)
    expect(onNavigate).toHaveBeenCalledWith('C:\\Users\\alice')
  })

  it('clicking a breadcrumb segment navigates to the cumulative Windows path', () => {
    const onNavigate = vi.fn()
    render(() => (
      <FileBrowser
        currentPath="C:\\Users\\alice\\proj"
        entries={[]}
        loading={false}
        error={null}
        onNavigate={onNavigate}
        onFileSelect={vi.fn()}
      />
    ))
    fireEvent.click(screen.getByRole('button', { name: 'alice' }))
    expect(onNavigate).toHaveBeenCalledWith('C:\\Users\\alice')
  })
})

describe('fileBrowser breadcrumbs with POSIX paths (regression)', () => {
  it('still splits a POSIX path with forward-slash separators', () => {
    const onNavigate = vi.fn()
    render(() => (
      <FileBrowser
        currentPath="/home/alice/proj"
        entries={[]}
        loading={false}
        error={null}
        onNavigate={onNavigate}
        onFileSelect={vi.fn()}
      />
    ))
    expect(screen.getByRole('button', { name: 'home' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'alice' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'proj' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'alice' }))
    expect(onNavigate).toHaveBeenCalledWith('/home/alice')
  })
})
