import type { Node } from '@milkdown/prose/model'
import { stripNeedlessEscapes } from './needlessEscapes'

/** Recognize blank paragraphs without treating empty structured blocks as blank prose. */
export function isBlankEditorDocument(document: Node): boolean {
  const schema = document.type.schema
  if (document.type !== schema.topNodeType)
    throw new Error('Editor content requires the schema document root.')
  let blank = true
  document.descendants((node) => {
    if (!blank)
      return false
    if (node.isText) {
      blank = !node.text?.trim()
      return false
    }
    if (node.type === schema.nodes.paragraph || node.type === schema.nodes.hardbreak)
      return true
    blank = false
    return false
  })
  return blank
}

/** Empty paragraphs can serialize as HTML breaks. They still contain no message. */
export function serializeEditorDocument(document: Node, serialize: (document: Node) => string): string {
  return isBlankEditorDocument(document) ? '' : stripNeedlessEscapes(serialize(document))
}
