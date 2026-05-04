import { describe, expect, it } from 'vitest'
import { decidePasteHandling } from './pasteDecision'

function makeFile(name = 'test.png', type = 'image/png'): File {
  return new File([new Uint8Array([0x89, 0x50, 0x4E, 0x47])], name, { type })
}

interface FakeClipboard {
  files: File[]
  items?: { kind: string, type: string, getAsFile: () => File | null }[]
  types?: string[]
}

function fakeDataTransfer(c: FakeClipboard): DataTransfer {
  return {
    files: c.files as unknown as FileList,
    items: (c.items ?? []) as unknown as DataTransferItemList,
    types: c.types ?? [],
  } as unknown as DataTransfer
}

describe('decidePasteHandling', () => {
  it('forwards files when DataTransfer.files is populated', () => {
    const file = makeFile()
    const dt = fakeDataTransfer({ files: [file], types: ['Files'] })
    const action = decidePasteHandling(dt, false)
    expect(action).toEqual({ kind: 'forward', files: [file] })
  })

  it('forwards files extracted from items when DataTransfer.files is empty', () => {
    const file = makeFile()
    const dt = fakeDataTransfer({
      files: [],
      items: [{ kind: 'file', type: 'image/png', getAsFile: () => file }],
      types: ['Files'],
    })
    const action = decidePasteHandling(dt, false)
    expect(action).toEqual({ kind: 'forward', files: [file] })
  })

  it('ignores non-file items', () => {
    const dt = fakeDataTransfer({
      files: [],
      items: [{ kind: 'string', type: 'text/plain', getAsFile: () => null }],
      types: ['text/plain'],
    })
    const action = decidePasteHandling(dt, false)
    expect(action).toEqual({ kind: 'defer' })
  })

  it('engages the Tauri-clipboard fallback for an entirely empty DataTransfer when running in Tauri', () => {
    // This is the WebKitGTK image-paste shape on Linux: clipboardData
    // arrives with types=[], items=[], files=[] even when the OS
    // clipboard holds a PNG. Without this branch the image is dropped.
    const dt = fakeDataTransfer({ files: [], items: [], types: [] })
    const action = decidePasteHandling(dt, true)
    expect(action).toEqual({ kind: 'tauri-clipboard' })
  })

  it('defers an empty DataTransfer when not running in Tauri', () => {
    const dt = fakeDataTransfer({ files: [], items: [], types: [] })
    const action = decidePasteHandling(dt, false)
    expect(action).toEqual({ kind: 'defer' })
  })

  it('defers when DataTransfer carries text — even in Tauri — so ProseMirror handles plain-text pastes', () => {
    const dt = fakeDataTransfer({
      files: [],
      items: [{ kind: 'string', type: 'text/plain', getAsFile: () => null }],
      types: ['text/plain'],
    })
    const action = decidePasteHandling(dt, true)
    expect(action).toEqual({ kind: 'defer' })
  })

  it('prefers DataTransfer.files over items when both are populated', () => {
    const fromFiles = makeFile('from-files.png')
    const fromItems = makeFile('from-items.png')
    const dt = fakeDataTransfer({
      files: [fromFiles],
      items: [{ kind: 'file', type: 'image/png', getAsFile: () => fromItems }],
      types: ['Files'],
    })
    const action = decidePasteHandling(dt, true)
    expect(action).toEqual({ kind: 'forward', files: [fromFiles] })
  })

  it('defers in Tauri when types is non-empty but files and items are empty', () => {
    // Real text pastes carry types like ['Files'] or ['text/plain'] — only
    // the all-empty shape signals the WebKitGTK image-paste case.
    const dt = fakeDataTransfer({ files: [], items: [], types: ['Files'] })
    const action = decidePasteHandling(dt, true)
    expect(action).toEqual({ kind: 'defer' })
  })
})
