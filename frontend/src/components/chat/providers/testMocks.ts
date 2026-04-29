import { vi } from 'vitest'
// Side-effect import: registers every provider plugin so renderer tests that
// only import the renderer under test still get `providerFor()` working.
// vi.mock calls below are hoisted by vitest, so the shiki mocks apply before
// any module loaded by `./index` reaches them.
import './index'

// Shared mocks for provider renderer tests. Importing this module registers
// stub implementations of modules that pull in shiki at evaluation time —
// shiki can't initialize under jsdom, so the real modules would crash on load.

// eslint-disable-next-line no-control-regex -- ANSI escape detection requires matching control characters
const ANSI_ESCAPE_RE = /\x1B\[[\d;]*m/

vi.mock('~/lib/renderAnsi', () => ({
  containsAnsi: (text: string) => ANSI_ESCAPE_RE.test(text),
  renderAnsi: (text: string) => `<pre class="shiki"><code>${text}</code></pre>`,
}))

vi.mock('~/lib/renderMarkdown', () => ({
  renderMarkdown: (text: string) => text,
  shikiHighlighter: { codeToHtml: (code: string) => `<pre><code>${code}</code></pre>` },
}))
