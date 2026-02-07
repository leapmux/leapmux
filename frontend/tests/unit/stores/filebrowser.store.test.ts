import type { FileInfo } from '~/generated/leapmux/v1/file_pb'
import { describe, expect, it } from 'vitest'
import { createFileBrowserStore } from '~/stores/filebrowser.store'

describe('fileBrowserStore', () => {
  it('initializes with default state', () => {
    const store = createFileBrowserStore()
    expect(store.state.currentPath).toBe('.')
    expect(store.state.entries).toEqual([])
    expect(store.state.loading).toBe(false)
    expect(store.state.error).toBeNull()
  })

  it('sets path and entries', () => {
    const store = createFileBrowserStore()
    const entries: FileInfo[] = [
      { $typeName: 'leapmux.v1.FileInfo', name: 'src', path: '/src', isDir: true, size: 0n, modTime: '', permissions: '' },
      { $typeName: 'leapmux.v1.FileInfo', name: 'main.go', path: '/main.go', isDir: false, size: 1024n, modTime: '', permissions: '' },
    ]
    store.setPath('/project')
    store.setEntries(entries)
    expect(store.state.currentPath).toBe('/project')
    expect(store.state.entries).toHaveLength(2)
    expect(store.state.entries[0].name).toBe('src')
  })

  it('tracks loading and error states', () => {
    const store = createFileBrowserStore()
    store.setLoading(true)
    expect(store.state.loading).toBe(true)
    store.setError('connection failed')
    expect(store.state.error).toBe('connection failed')
    store.setLoading(false)
    store.setError(null)
    expect(store.state.loading).toBe(false)
    expect(store.state.error).toBeNull()
  })
})
