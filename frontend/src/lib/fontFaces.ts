/**
 * The four faces of a font family, and the guarded load of one face. The terminal
 * and the chat both load every face of their families before they measure text.
 */

// A renderer draws a separate glyph for each (weight, style) combination, so a
// caller has to trigger fetches for all four variants -- not just the regular
// face that it happens to paint first.
export const FONT_VARIANTS: ReadonlyArray<{ weight: string, style: string }> = [
  { weight: 'normal', style: 'normal' },
  { weight: 'bold', style: 'normal' },
  { weight: 'normal', style: 'italic' },
  { weight: 'bold', style: 'italic' },
]

// Cold-start font fetches can fail transiently -- e.g. Tauri's static-file
// server is briefly unavailable right after launch. Retry a failed variant a
// bounded number of times with exponential backoff, then give up so a
// genuinely-missing font doesn't spin forever.
export const FONT_LOAD_MAX_RETRIES = 4
export const FONT_LOAD_RETRY_BASE_MS = 300

/** Load one font variant, resolving to whether it settled without a fetch error. */
export function tryLoadFontVariant(spec: string): Promise<boolean> {
  // A resolve (even with zero matched faces, e.g. a pure system-font family)
  // means "nothing failed"; only a reject signals a matched @font-face whose
  // fetch failed and is worth retrying.
  //
  // `document.fonts.load` throws a SyntaxError *synchronously* when `spec` is
  // not a parseable CSS font shorthand (e.g. a malformed custom family from a
  // corrupted preference). Catch it so a bad spec degrades to a failed variant
  // rather than throwing into the caller. For the terminal that would reject
  // `fontsReady`, which throws into the pool's attach and, at construction, out
  // of createTerminalInstance itself.
  try {
    return document.fonts.load(spec).then(() => true, () => false)
  }
  catch {
    return Promise.resolve(false)
  }
}

/**
 * One CSS font shorthand `<style> <weight> <size> <family>` for each face of
 * `fontFamily`. `size` is a CSS length, such as `14px` or `1em`.
 */
export function fontVariantSpecs(fontFamily: string, size: string): string[] {
  return FONT_VARIANTS.map(({ weight, style }) => `${style} ${weight} ${size} ${fontFamily}`)
}

/**
 * Whether the browser can draw `spec` now, with no load. False when the document
 * has no font API, and when the font parser refuses `spec`.
 */
export function fontVariantAvailable(spec: string): boolean {
  try {
    return document.fonts.check(spec)
  }
  catch {
    return false
  }
}
