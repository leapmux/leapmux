import type { FileEditDiffSource } from '../../results/fileEditDiff'
import { isObject } from '~/lib/jsonPick'

const RECEIPT_HEADER = '\nActual replacement receipt after write:\n'
const REPLACEMENT_HEADER = /^@@ replacement (\d+) of (\d+) \((\d+) occurrence\(s\)(, fuzzy match(?:, first matched sample shown)?)?\) @@$/
const SPAN_TRUNCATED = '…[replacement span truncated]…'

interface ReplacementRequest {
  old_string: string
  new_string: string
}

interface ReplacementReceipt {
  index: number
  total: number
  occurrences: number
  fuzzy: boolean
  old: string[]
  added: string[]
}

function replacementRequest(value: unknown): ReplacementRequest | undefined {
  return isObject(value) && typeof value.old_string === 'string' && value.old_string !== '' && typeof value.new_string === 'string'
    ? { old_string: value.old_string, new_string: value.new_string }
    : undefined
}

/** The native receipt omits the final newline and uses the same marker for empty text and literal text. */
function spanText(lines: string[], requested: string | undefined, fuzzy: boolean, mayBeEmpty: boolean): string | undefined {
  if (!lines.length)
    return undefined
  const text = lines.join('\n').replaceAll('\r\n', '\n')
  if (requested !== undefined && !fuzzy) {
    const normalized = requested.replaceAll('\r\n', '\n')
    const encoded = normalized === '' ? '<empty>' : normalized.replace(/\n$/, '')
    if (encoded === text)
      return normalized
    const fragments = text.split(`\n${SPAN_TRUNCATED}\n`)
    if (fragments.length === 2 && encoded.startsWith(fragments[0]) && encoded.endsWith(fragments[1])
      && encoded.length >= fragments[0].length + fragments[1].length) {
      return normalized
    }
  }
  if (text.includes(SPAN_TRUNCATED) || (mayBeEmpty && text === '<empty>'))
    return undefined
  return text
}

function parseReceipts(body: string): ReplacementReceipt[] | undefined {
  const receipts: ReplacementReceipt[] = []
  let active: ReplacementReceipt | undefined
  for (const line of body.split('\n')) {
    const header = REPLACEMENT_HEADER.exec(line)
    if (header) {
      const index = Number(header[1])
      const total = Number(header[2])
      const occurrences = Number(header[3])
      if (![index, total, occurrences].every(value => Number.isSafeInteger(value) && value > 0)
        || index > total || (active && (index <= active.index || total !== active.total))) {
        return undefined
      }
      active = { index, total, occurrences, fuzzy: !!header[4], old: [], added: [] }
      receipts.push(active)
    }
    else if (active && line.startsWith('-') && active.added.length === 0) {
      active.old.push(line.slice(1))
    }
    else if (active && line.startsWith('+') && active.old.length > 0) {
      active.added.push(line.slice(1))
    }
    else if (line !== '' && !/^…\[\d+ intermediate replacement receipt\(s\) omitted\]…$/.test(line)) {
      return undefined
    }
  }
  return receipts.length && receipts.every(receipt => receipt.old.length && receipt.added.length) ? receipts : undefined
}

/** Prefer actual matched text. Recover omitted exact text only from its matching request. */
export function reasonixEditReceipt(output: string, filePath: string, input: Record<string, unknown> = {}): FileEditDiffSource[] | null {
  const position = output.indexOf(RECEIPT_HEADER)
  if (position < 0)
    return null
  const receipts = parseReceipts(output.slice(position + RECEIPT_HEADER.length))
  if (!receipts)
    return []
  const requests = Array.isArray(input.edits) ? input.edits.map(replacementRequest) : [replacementRequest(input)]
  const total = receipts[0].total
  const summary = output.slice(0, position)
  const batch = /^multi_edit .+: (\d+) edits applied \(\d+ total replacements\)$/.exec(summary)
  const canRecoverMissing = batch && Number(batch[1]) === total && requests.length === total && requests.every(Boolean)
  if (receipts.length !== total && !canRecoverMissing)
    return []
  const byIndex = new Map(receipts.map(receipt => [receipt.index, receipt]))
  const sources: FileEditDiffSource[] = []
  for (let index = 1; index <= total; index++) {
    const receipt = byIndex.get(index)
    const request = requests.length === total ? requests[index - 1] : undefined
    const oldStr = receipt ? spanText(receipt.old, request?.old_string, receipt.fuzzy, false) : request?.old_string
    const newStr = receipt ? spanText(receipt.added, request?.new_string, false, true) : request?.new_string
    if (oldStr === undefined || newStr === undefined)
      return []
    const notes = [
      receipt?.fuzzy ? 'Fuzzy match' : '',
      receipt && receipt.occurrences > 1 ? `${receipt.occurrences} replacements; ${receipt.fuzzy ? 'first matched sample' : 'one sample'} shown` : '',
    ].filter(Boolean)
    sources.push({ filePath, structuredPatch: null, oldStr, newStr, showLineNumbers: false, notice: notes.length ? notes.join('; ') : undefined })
  }
  return sources
}
