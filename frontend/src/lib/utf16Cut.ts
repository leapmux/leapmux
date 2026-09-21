function clampedIndex(text: string, index: number): number {
  return Math.min(Math.max(index, 0), text.length)
}

function splitsSurrogatePair(text: string, index: number): boolean {
  if (index <= 0 || index >= text.length)
    return false
  const previous = text.charCodeAt(index - 1)
  const next = text.charCodeAt(index)
  return previous >= 0xD800 && previous <= 0xDBFF && next >= 0xDC00 && next <= 0xDFFF
}

/** Move a UTF-16 cut backward when it splits a surrogate pair. */
export function snapUtf16CutBackward(text: string, index: number): number {
  const cut = clampedIndex(text, index)
  return splitsSurrogatePair(text, cut) ? cut - 1 : cut
}

/** Move a UTF-16 cut forward when it splits a surrogate pair. */
export function snapUtf16CutForward(text: string, index: number): number {
  const cut = clampedIndex(text, index)
  return splitsSurrogatePair(text, cut) ? cut + 1 : cut
}
