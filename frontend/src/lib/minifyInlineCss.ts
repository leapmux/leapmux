import { transform } from 'lightningcss'

/**
 * Minify CSS for an inline `<style>` at emit time.
 *
 * Source strings stay readable; the document head receives the compact form.
 * Server-only — do not import from client modules (native lightningcss).
 */
export function minifyInlineCss(css: string): string {
  const { code } = transform({
    filename: 'inline.css',
    code: new TextEncoder().encode(css),
    minify: true,
  })
  return new TextDecoder().decode(code)
}
