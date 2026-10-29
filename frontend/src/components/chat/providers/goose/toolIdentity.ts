import { pickObject, pickString } from '~/lib/jsonPick'

/** Read the stable tool identity from Goose metadata, including prefixed tool names. */
export function gooseToolIdentity(tool: Record<string, unknown>): { name: string, extension: string } {
  const metadata = pickObject(pickObject(pickObject(tool, '_meta'), 'goose'), 'toolCall')
  const fullName = pickString(metadata, 'toolName')
  const separator = fullName.indexOf('__')
  return {
    name: separator >= 0
      ? fullName.slice(separator + 2)
      : fullName,
    extension: pickString(metadata, 'extensionName') || (separator >= 0
      ? fullName.slice(0, separator)
      : ''),
  }
}
