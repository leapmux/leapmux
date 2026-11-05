import type { FileEditDiff } from '../../../model/fileEditDiff'
import type { AntigravityStep } from './toolCommon'
import { ANTIGRAVITY_TOOL } from '~/generated/contracts/antigravity-protocol'
import { pickObject, pickString } from '~/lib/jsonPick'
import { fileEditDiffFromOldNew, fileEditDiffFromUnifiedPatch, fileEditDiffFromWholeFile } from '../../../model/fileEditDiff'
import { filePathFromURI } from './read'

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
  const path = filePathFromURI(pickString(pickObject(command, 'file'), 'absoluteUri'))
  const lines = pickObject(pickObject(pickObject(action, 'actionResult'), 'edit'), 'diff')?.unifiedDiff
  const entries = pickObject(lines as Record<string, unknown> | undefined, 'lines')
  if (!path || !Array.isArray(entries))
    return null
  const patch = entries.flatMap((entry) => {
    if (typeof entry !== 'object' || entry === null)
      return []
    const record = entry as Record<string, unknown>
    const text = pickString(record, 'text')
    const type = pickString(record, 'type')
    const prefix = type.includes('ADD')
      ? '+'
      : type.includes('DELETE') || type.includes('REMOVE')
        ? '-'
        : ' '
    return [`${prefix}${text}`]
  }).join('\n')
  const change = fileEditDiffFromUnifiedPatch(path, patch)
  return change
    ? [
        { ...change, operation: 'edit' },
      ]
    : null
}
