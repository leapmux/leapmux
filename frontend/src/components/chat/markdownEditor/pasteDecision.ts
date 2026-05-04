/**
 * Decision returned by `decidePasteHandling`; see the call site for the
 * `tauri-clipboard` rationale.
 */
export type PasteAction
  = | { kind: 'forward', files: File[] }
    | { kind: 'tauri-clipboard' }
    | { kind: 'defer' }

export function decidePasteHandling(dt: DataTransfer, isTauri: boolean): PasteAction {
  const directFiles = [...dt.files]
  if (directFiles.length > 0)
    return { kind: 'forward', files: directFiles }

  const itemFiles = [...(dt.items ?? [])]
    .filter(it => it.kind === 'file')
    .map(it => it.getAsFile())
    .filter((f): f is File => f !== null)
  if (itemFiles.length > 0)
    return { kind: 'forward', files: itemFiles }

  if (isTauri && dt.types.length === 0)
    return { kind: 'tauri-clipboard' }

  return { kind: 'defer' }
}
