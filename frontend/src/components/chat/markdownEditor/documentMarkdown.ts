import type { Node } from '@milkdown/prose/model'
import { stripNeedlessEscapes } from './needlessEscapes'

/** Recognize blank paragraphs without treating empty structured blocks as blank prose. */
export function isBlankEditorDocument(document: Node): boolean {
  const schema = document.type.schema
  if (document.type !== schema.topNodeType)
    throw new Error('Editor content requires the schema document root.')
  const isBlank = (node: Node): boolean => {
    if (node.isText)
      return !node.text?.trim()
    if (node.type !== schema.topNodeType && node.type !== schema.nodes.paragraph && node.type !== schema.nodes.hardbreak)
      return false
    for (let index = 0; index < node.childCount; index++) {
      if (!isBlank(node.child(index)))
        return false
    }
    return true
  }
  return isBlank(document)
}

/** Empty paragraphs can serialize as HTML breaks. They still contain no message. */
export function serializeEditorDocument(document: Node, serialize: (document: Node) => string): string {
  return isBlankEditorDocument(document) ? '' : stripNeedlessEscapes(serialize(document))
}
