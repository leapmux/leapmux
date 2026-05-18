/**
 * Write `text` to the system clipboard, ignoring empty inputs and
 * environments without a clipboard API. Errors (e.g. permission denied
 * on a non-secure context) are swallowed — auto-copy is a convenience,
 * not a contract.
 */
export function copyTextToClipboard(text: string): void {
  if (text.length === 0)
    return
  if (typeof navigator === 'undefined' || !navigator.clipboard?.writeText)
    return
  void navigator.clipboard.writeText(text).catch(() => {})
}
