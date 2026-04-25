import type { JSX } from 'solid-js'

// Inline-icon CSS used by brand-mark SVG icons (AgentProviderIcon,
// EditorIcons). Locks the rendered glyph to its declared `size` so flex
// containers don't squeeze it, and aligns it on the text baseline.
export function iconStyle(size: number): JSX.CSSProperties {
  return {
    'flex-shrink': '0',
    'min-width': `${size}px`,
    'min-height': `${size}px`,
    'vertical-align': 'middle',
  }
}
