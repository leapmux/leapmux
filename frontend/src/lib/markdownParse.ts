import remarkGfm from 'remark-gfm'
import remarkParse from 'remark-parse'
import { unified } from 'unified'

/**
 * Shared remark-parse + remark-gfm parser used by both the markdown render
 * pipeline and markdown-aware truncation. Keeping parse config in one place
 * makes "can't drift from render behavior" structural rather than by convention.
 */
export function createMarkdownParser() {
  return unified()
    .use(remarkParse)
    .use(remarkGfm)
}
