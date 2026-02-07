import type { FileInfo } from '~/generated/leapmux/v1/file_pb'
import { createStore } from 'solid-js/store'

interface FileBrowserStoreState {
  currentPath: string
  entries: FileInfo[]
  loading: boolean
  error: string | null
}

export function createFileBrowserStore() {
  const [state, setState] = createStore<FileBrowserStoreState>({
    currentPath: '.',
    entries: [],
    loading: false,
    error: null,
  })

  return {
    state,

    setPath(path: string) {
      setState('currentPath', path)
    },

    setEntries(entries: FileInfo[]) {
      setState('entries', entries)
    },

    setLoading(loading: boolean) {
      setState('loading', loading)
    },

    setError(error: string | null) {
      setState('error', error)
    },
  }
}
