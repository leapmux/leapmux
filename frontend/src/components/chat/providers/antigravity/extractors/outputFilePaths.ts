import type { ToolCall } from '../../../model/toolCall'
import type { RowExtractionInput } from '~/components/chat/rowExtractionTypes'
import { pickObject, pickString } from '~/lib/jsonPick'
import { fileUriToPath } from '~/lib/paths'

export function antigravityOutputFilePaths(input: RowExtractionInput, _call?: ToolCall): readonly string[] {
  const generic = pickObject(input.resolved.parentObject, 'generic')
  const result = pickObject(generic, 'result')
  const output = pickString(pickObject(pickObject(input.resolved.parentObject, 'runCommand'), 'combinedOutput'), 'full')
  const uri = /file:\/\/[^\\\s]+output\.txt/.exec(output)?.[0] || pickString(result, 'fullOutputUri')
  const path = fileUriToPath(uri) ?? uri
  return path
    ? [path]
    : []
}
