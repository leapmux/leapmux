import type { FileEditDiff } from '../../../model/fileEditDiff'
import type { AntigravityStep } from './toolCommon'
import { ANTIGRAVITY_TOOL } from '~/generated/contracts/antigravity-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { fileUriToPath } from '~/lib/paths'
import { contextualPatchHunks } from '../../../diff/diffBuilder'
import { fileEditDiffFromHunks, fileEditDiffFromOldNew, fileEditDiffFromWholeFile } from '../../../model/fileEditDiff'

export function antigravityFileChange(step: AntigravityStep): FileEditDiff[] {
  const name = String(step.toolCall.name ?? '')
  const path = String(step.args.Path ?? '')
  if (!path)
    return []
  if (name === ANTIGRAVITY_TOOL.WriteFile) {
    return [
      fileEditDiffFromWholeFile(path, String(step.args.Content ?? ''), 'add'),
    ]
  }
  return [
    {
      ...fileEditDiffFromOldNew(path, String(step.args.Target ?? ''), String(step.args.Replacement ?? '')),
      operation: 'edit',
    },
  ]
}

export function antigravityLandedChanges(step: AntigravityStep): FileEditDiff[] | null {
  const action = pickObject(step.raw, 'codeAction')
  const command = pickObject(pickObject(action, 'actionSpec'), 'command')
  const edit = pickObject(pickObject(action, 'actionResult'), 'edit')
  const uri = pickString(edit, 'absoluteUri') || pickString(pickObject(command, 'file'), 'absoluteUri')
  const path = fileUriToPath(uri) ?? uri
  const diff = pickObject(edit, 'diff')
  const entries = pickObject(diff, 'unifiedDiff')?.lines
  const startLine = diff?.startLine ?? 0
  if (!path || !Array.isArray(entries) || entries.length === 0
    || typeof startLine !== 'number' || !Number.isSafeInteger(startLine) || startLine < 0) {
    return null
  }
  const lines: string[] = []
  const originalLines: string[] = []
  for (const entry of entries) {
    if (!isObject(entry) || (entry.text !== undefined && entry.text !== null && typeof entry.text !== 'string'))
      return null
    const type = pickString(entry, 'type')
    let prefix: string
    switch (type) {
      case 'UNIFIED_DIFF_LINE_TYPE_INSERT':
        prefix = '+'
        break
      case 'UNIFIED_DIFF_LINE_TYPE_DELETE':
        prefix = '-'
        break
      case 'UNIFIED_DIFF_LINE_TYPE_UNCHANGED':
        prefix = ' '
        break
      default:
        return null
    }
    for (const text of pickString(entry, 'text').split('\n')) {
      lines.push(prefix + text)
      if (prefix !== '+')
        originalLines.push(text)
    }
  }
  if (!Number.isSafeInteger(startLine + lines.length))
    return null
  const change = fileEditDiffFromHunks(path, contextualPatchHunks(lines, startLine))
  // A full native block includes an empty record for the final newline.
  if (startLine === 0 && originalLines.at(-1) === '')
    originalLines.pop()
  return [
    {
      ...change,
      operation: 'edit',
      originalFile: { startLineNumber: startLine + 1, lines: originalLines },
    },
  ]
}
