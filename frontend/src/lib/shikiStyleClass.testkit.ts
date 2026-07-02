/**
 * Read the shared token-style rules that shikiStyleClass injected into this thread's
 * dedicated `<style>`, normalized to the compact `.selector{decl}` form the assertions use.
 *
 * Production injects via CSSOM `insertRule` (an O(1) append; a per-rule `textContent +=`
 * re-parsed the whole sheet and was O(N^2) over distinct declarations), so the rules live in
 * the element's `sheet`, NOT its `textContent`. jsdom serializes each rule's `cssText` with
 * its own spacing (`.sel { --a: #123; }`); this strips that back to `.sel{--a:#123}` so tests
 * can `.toContain('.sk-xxxx{--shiki-light:#123;...}')` regardless of the serializer's spacing.
 * Returns '' when nothing has been injected (no element / no sheet).
 */
export function readInjectedShikiRules(): string {
  const el = document.querySelector<HTMLStyleElement>('style[data-shiki-style-classes]')
  if (!el?.sheet)
    return ''
  return Array.from(el.sheet.cssRules)
    .map(rule => rule.cssText.replace(/\s+/g, '').replace(/;\}/g, '}'))
    .join('')
}
