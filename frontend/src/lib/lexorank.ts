/**
 * Lexicographic ranking for ordered items.
 * Port of hub/internal/lexorank/lexorank.go.
 */

const MIN_CHAR = 'a'
const MAX_CHAR = 'z'
const MID_CHAR = 'n'

/** Returns an initial rank suitable for the first item. */
export function first(): string {
  return MID_CHAR
}

/** Returns a rank that sorts after s. */
export function after(s: string): string {
  return s + MID_CHAR
}

/**
 * Returns a rank between a and b.
 * If a is empty, returns a rank before b.
 * If b is empty, returns a rank after a.
 * If both are empty, returns first().
 */
export function mid(a: string, b: string): string {
  if (!a && !b)
    return first()
  if (!a)
    return before(b)
  if (!b)
    return after(a)
  return between(a, b)
}

function before(s: string): string {
  if (!s)
    return first()
  const last = s.charCodeAt(s.length - 1)
  const minCode = MIN_CHAR.charCodeAt(0)
  if (last > minCode + 1) {
    const midCode = Math.floor((minCode + last) / 2)
    return s.slice(0, -1) + String.fromCharCode(midCode)
  }
  return s.slice(0, -1) + MIN_CHAR + MID_CHAR
}

function between(a: string, b: string): string {
  const maxLen = Math.max(a.length, b.length)
  const pa = padRight(a, maxLen)
  const pb = padRight(b, maxLen)

  for (let i = 0; i < maxLen; i++) {
    const ca = pa.charCodeAt(i)
    const cb = pb.charCodeAt(i)

    if (ca === cb)
      continue

    if (cb - ca > 1) {
      const midCode = Math.floor((ca + cb) / 2)
      return pa.slice(0, i) + String.fromCharCode(midCode)
    }

    // Adjacent characters - recurse deeper
    const suffix = between(
      trimTrailing(pa.slice(i + 1), MIN_CHAR),
      MAX_CHAR,
    )
    return pa.slice(0, i + 1) + suffix
  }

  // Strings are equal - append midChar
  return a + MID_CHAR
}

function padRight(s: string, length: number): string {
  while (s.length < length)
    s += MIN_CHAR
  return s
}

function trimTrailing(s: string, c: string): string {
  let end = s.length
  while (end > 0 && s[end - 1] === c)
    end--
  return s.slice(0, end)
}
