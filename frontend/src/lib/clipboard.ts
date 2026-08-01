/**
 * Write `text` to the system clipboard, reporting whether it landed.
 *
 * Never rejects and never throws: an empty input, a missing clipboard API (any
 * non-secure origin — plain `http://` on a LAN — has no `navigator.clipboard`),
 * a denied permission, or an unfocused document all resolve `false`. Callers
 * that treat copying as a convenience can ignore the result with `void`; a
 * caller that reports success to the user should await it, so it cannot claim
 * to have copied something it did not.
 */
export async function copyTextToClipboard(text: string): Promise<boolean> {
  if (text.length === 0)
    return false
  if (typeof navigator === 'undefined' || !navigator.clipboard?.writeText)
    return false
  try {
    await navigator.clipboard.writeText(text)
    return true
  }
  catch {
    return false
  }
}
