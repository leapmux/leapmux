import { isObject } from '../../../src/lib/jsonPick'

/** Read text parts without treating function arguments or responses as text. */
export function googlePartsText(parts: unknown): string {
  if (!Array.isArray(parts))
    return ''
  return parts.filter(isObject).flatMap(part => typeof part.text === 'string' ? [part.text] : []).join('\n')
}

/** Read the last user prompt. Function-response-only rows preserve the preceding prompt. */
export function googleLastUserText(contents: unknown): string {
  if (!Array.isArray(contents))
    return ''
  for (let index = contents.length - 1; index >= 0; index--) {
    const row: unknown = contents[index]
    if (!isObject(row) || row.role !== 'user')
      continue
    const text = googlePartsText(row.parts)
    if (text !== '')
      return text
  }
  return ''
}

/** Read exact function declarations in their native order. */
export function googleFunctionDeclarations(tools: unknown): Record<string, unknown>[] {
  if (!Array.isArray(tools))
    throw new Error('The native Google model request contains no tool catalog.')
  return tools.flatMap((tool: unknown) => {
    if (!isObject(tool) || !Array.isArray(tool.functionDeclarations))
      throw new Error('The native Google tool catalog contains an invalid entry.')
    return tool.functionDeclarations.map((declaration: unknown) => {
      if (!isObject(declaration) || typeof declaration.name !== 'string' || declaration.name === '')
        throw new Error('The native Google function declaration has no valid name.')
      return declaration
    })
  })
}
