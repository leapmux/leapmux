import type { FileInfo } from '~/generated/leapmux/v1/file_pb'
import { render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { FileBrowser } from '~/components/filebrowser/FileBrowser'

function makeEntry(name: string, isDir: boolean, size: bigint = 0n): FileInfo {
  return {
    $typeName: 'leapmux.v1.FileInfo',
    name,
    path: `/${name}`,
    isDir,
    size,
    modTime: '',
    permissions: '',
  }
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
