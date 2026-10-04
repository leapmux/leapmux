import { isObject, pickObject } from '~/lib/jsonPick'
import { shallowEqual } from '~/lib/shallowEqual'

/** Compare ordered native text and image blocks, including image reference values. */
export function deepseekHarnessContentEqual(left: unknown, right: unknown): boolean {
  if (!Array.isArray(left) || !Array.isArray(right) || left.length !== right.length)
    return false
  for (let index = 0; index < left.length; index++) {
    const block: unknown = left[index]
    const other: unknown = right[index]
    if (!isObject(block) || !isObject(other) || block.type !== other.type)
      return false
    if (block.type === 'text') {
      if (typeof block.text !== 'string' || !shallowEqual(block, other))
        return false
      continue
    }
    if (block.type !== 'image')
      return false
    const attachment = pickObject(block, 'attachment')
    const otherAttachment = pickObject(other, 'attachment')
    if (!attachment || !otherAttachment || !shallowEqual(attachment, otherAttachment)
      || !shallowEqual({ ...block, attachment: null }, { ...other, attachment: null })) {
      return false
    }
  }
  return true
}
