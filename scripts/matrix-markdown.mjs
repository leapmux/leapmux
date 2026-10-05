import { createRequire } from 'node:module'

// The frontend workspace owns the Markdown packages. This file reads them from there, so the
// repository root needs no package.json of its own.
const require = createRequire(new URL('../frontend/package.json', import.meta.url))
const { unified } = require('unified')
const remarkParse = require('remark-parse').default

const markdown = unified().use(remarkParse)

/** Parse a note with the CommonMark parser. Hugo renders a note with the CommonMark parser Goldmark. */
export function parseMarkdown(source) {
  return markdown.parse(source)
}

/** Call `visitor` for a node and for each of its descendants, parents first. */
export function walkMarkdown(node, visitor) {
  visitor(node)
  for (const child of node.children ?? [])
    walkMarkdown(child, visitor)
}

/** The plain text of one node. A space separates two inline nodes, as in the rendered page text. */
function nodeText(node) {
  return typeof node.value === 'string'
    ? node.value
    : (node.children ?? []).map(nodeText).join(' ')
}

/** The plain text of each top-level block of a note: one entry for each paragraph. */
export function markdownBlockTexts(source) {
  return parseMarkdown(source).children.map(nodeText)
}

/** The plain text of a note. */
export function markdownText(source) {
  return markdownBlockTexts(source).join(' ')
}

/** The target of each link in a note, in order. */
export function markdownLinks(source) {
  const urls = []
  walkMarkdown(parseMarkdown(source), (node) => {
    if (node.type === 'link')
      urls.push(node.url)
  })
  return urls
}
