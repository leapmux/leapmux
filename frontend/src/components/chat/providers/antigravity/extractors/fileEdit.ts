import type { FileEditDiff } from '../../../model/fileEditDiff'
import type { AntigravityStep } from './toolCommon'
import { ANTIGRAVITY_TOOL } from '~/generated/contracts/antigravity-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { fileUriToPath } from '~/lib/paths'
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
  const uri = pickString(pickObject(command, 'file'), 'absoluteUri')
  const path = fileUriToPath(uri) ?? uri
  const diff = pickObject(pickObject(pickObject(action, 'actionResult'), 'edit'), 'diff')
  const entries = pickObject(diff, 'unifiedDiff')?.lines
  if (!path || !Array.isArray(entries) || entries.length === 0)
    return null
  const lines: string[] = []
  let oldLines = 0
  let newLines = 0
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
        oldLines++
      if (prefix !== '-')
        newLines++
    }
  }
  const change = fileEditDiffFromHunks(path, [
    {
      oldStart: oldLines ? 1 : 0,
      oldLines,
      newStart: newLines ? 1 : 0,
      newLines,
      lines,
    },
  ])
  return [
    { ...change, operation: 'edit', showLineNumbers: false },
  ]
}
