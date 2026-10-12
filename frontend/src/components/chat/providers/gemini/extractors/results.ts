import type { CommandResult } from '../../../model/commandResult'
import type { McpContentItem } from '../../../model/mcpToolCall'
import type { ImageResultSource } from '~/lib/imageBlocks'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { stripAnsi } from '~/lib/renderAnsi'

/** Read Gemini's complete native function response parts. */
export function geminiResultParts(record: Record<string, unknown>): Record<string, unknown>[] {
  if (!Array.isArray(record.result))
    return []
  return record.result.filter(isObject).flatMap((part) => {
    const response = pickObject(part, 'functionResponse')
    const nested = Array.isArray(response?.parts) ? response.parts.filter(isObject) : []
    return [part, ...nested]
  })
}

/** Remove only the outer wrapper that Gemini adds to an untrusted response. */
export function geminiUntrustedText(text: string): string {
  const start = '<untrusted_context>\n'
  const end = '\n</untrusted_context>'
  return text.startsWith(start) && text.endsWith(end) ? text.slice(start.length, -end.length) : text
}

export function geminiResultText(record: Record<string, unknown>): string {
  const text = geminiResultParts(record).flatMap((part) => {
    const response = pickObject(pickObject(part, 'functionResponse'), 'response')
    if (typeof response?.output === 'string')
      return [geminiUntrustedText(response.output)]
    if (typeof response?.error === 'string')
      return [response.error]
    return typeof part.text === 'string' ? [part.text] : []
  })
  return text.join('\n')
}

export function geminiResultImages(record: Record<string, unknown>, filePath?: string): ImageResultSource[] {
  return geminiResultParts(record).flatMap((part) => {
    const inline = pickObject(part, 'inlineData')
    if (typeof inline?.data !== 'string' || !pickString(inline, 'mimeType').startsWith('image/'))
      return []
    return [{ data: inline.data, mimeType: pickString(inline, 'mimeType'), ...(filePath ? { filePath } : {}) }]
  })
}

export function geminiResultContent(record: Record<string, unknown>): McpContentItem[] {
  const text = geminiResultText(record)
  return [
    ...(text !== '' ? [{ type: 'text' as const, text }] : []),
    ...geminiResultImages(record).map(source => ({ type: 'image' as const, source })),
  ]
}

function geminiAnsiText(display: unknown): string | null {
  if (!Array.isArray(display))
    return null
  const rows = display.map((row: unknown) => {
    if (!Array.isArray(row))
      return null
    const cells = row.map((cell: unknown) => isObject(cell) && typeof cell.text === 'string' ? cell.text : null)
    return cells.includes(null) ? null : cells.join('').trimEnd()
  })
  return rows.every((row): row is string => row !== null) ? rows.join('\n') : null
}

function sameGeminiDisplay(output: string, display: string, ansi: boolean): boolean {
  if (!ansi)
    return output === display
  return stripAnsi(output).replace(/\s+/g, '') === display.replace(/\s+/g, '')
}

/** Compare the native display with its report before separating shell metadata. */
export function geminiShellResult(record: Record<string, unknown>): CommandResult {
  const report = geminiResultText(record)
  const display = typeof record.resultDisplay === 'string' ? record.resultDisplay : geminiAnsiText(record.resultDisplay)
  const ansi = typeof record.resultDisplay !== 'string'
  const failed = record.status === 'error'
  const wrapper = /^Output: ([\s\S]*)\nProcess Group PGID: [1-9]\d*$/.exec(report)
  if (!wrapper || display === null)
    return { output: typeof record.resultDisplay === 'string' ? record.resultDisplay : report, ...(failed ? { failed: true } : {}) }

  const body = wrapper[1] ?? ''
  if (sameGeminiDisplay(body, display, ansi))
    return { output: body, ...(failed ? { failed: true } : {}) }
  if (body === '(empty)' && display === '')
    return { output: '', ...(failed ? { failed: true } : {}) }

  const exit = /^([\s\S]*)\nExit Code: (-?\d+)$/.exec(body)
  if (exit) {
    const output = exit[1] ?? ''
    const code = Number(exit[2])
    const emptyFailure = output === '(empty)' && display === `Command exited with code: ${code}`
    if (Number.isSafeInteger(code) && (emptyFailure || sameGeminiDisplay(output, display, ansi)))
      return { output: emptyFailure ? '' : output, exitCode: code }
  }
  // Unknown native report fields stay in the output. A report alone cannot identify stdout that resembles those fields.
  return { output: body, ...(failed ? { failed: true } : {}) }
}
