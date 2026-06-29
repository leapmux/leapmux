import { vi } from 'vitest'
// Side-effect import: registers every provider plugin so renderer tests that
// only import the renderer under test still get `providerFor()` working.
// vi.mock calls below are hoisted by vitest, so the shiki mocks apply before
// any module loaded by `./index` reaches them.
import './index'

// Shared mocks for provider renderer tests. Importing this module registers
// stub implementations of modules that pull in shiki at evaluation time —
// shiki can't initialize under jsdom, so the real modules would crash on load.

vi.mock('~/lib/renderAnsi', () => ({
  containsAnsi: (text: string) => text.includes('\x1B'),
  renderAnsi: (text: string) => `<pre class="shiki"><code>${text}</code></pre>`,
}))

vi.mock('~/lib/renderMarkdown', () => ({
  getCachedMarkdownHtml: () => undefined,
  renderMarkdown: (text: string) => text,
  renderMarkdownCachedOrPlain: (text: string) => text,
  renderMarkdownPlain: (text: string) => text,
  shikiHighlighter: { codeToHtml: (code: string) => `<pre><code>${code}</code></pre>` },
}))
