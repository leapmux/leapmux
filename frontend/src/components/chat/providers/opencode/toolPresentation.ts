import type { ACPToolAdapter } from '../acp/toolPresentation'
import { isObject, pickBoolean, pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { pluralize } from '~/lib/plural'
import { rawTodosToItems } from '~/stores/chatTodos'
import { parseUnifiedDiffCached } from '../../diff'
import { fileEditDiffFromHunks, fileEditHasDiff } from '../../results/fileEditDiff'
import { readFileSourceFromContent } from '../../results/readFileResult'
import { openCodeSearchLines } from './searchOutput'
import { openCodeTaskResult } from './taskResult'

/** OpenCode and Kilo preserve display metadata alongside model-facing output. */
export const openCodeToolAdapter: ACPToolAdapter = (tool, initial) => {
  const write = initial.kind === 'edit' && typeof initial.input.content === 'string' && typeof initial.input.filePath === 'string'
  const search = initial.kind === 'search' && (tool.title === 'glob' || tool.title === 'grep') ? tool.title : undefined
  let model = write || search ? { ...initial, kind: write ? 'write' : search! } : initial
  const metadata = pickObject(pickObject(tool, 'rawOutput'), 'metadata')
  const task = openCodeTaskResult(model.output, metadata, model.input)
  if (tool.title === 'task' || (model.kind === 'think' && pickString(model.input, 'subagent_type')) || (task && pickString(metadata, 'sessionId'))) {
    return {
      ...model,
      kind: 'agent',
      label: 'Task',
      title: pickString(model.input, 'description') || 'Agent',
      agentRequest: { toolName: 'Task', description: pickString(model.input, 'description'), agentType: pickString(model.input, 'subagent_type'), prompt: pickString(model.input, 'prompt') },
      body: task ? { type: 'agent', source: task } : { type: 'text' },
    }
  }
  const rawTodos = Array.isArray(metadata?.todos) ? metadata.todos : model.input.todos
  if (tool.title === 'todowrite' || Array.isArray(metadata?.todos)) {
    const items = Array.isArray(rawTodos)
      ? rawTodosToItems(rawTodos.map(entry => isObject(entry) && entry.status === 'cancelled' ? { ...entry, status: 'deleted' } : entry))
      : []
    return {
      ...model,
      kind: 'todo',
      title: items.length ? pluralize(items.length, 'task') : 'To-do list',
      body: tool.status === 'failed' || tool.status === 'cancelled' || !Array.isArray(rawTodos) ? { type: 'text' } : { type: 'todo', items },
    }
  }
  if (model.kind === 'execute' && model.title === 'bash' && pickString(model.input, 'command'))
    model = { ...model, title: '' }
  if (tool.status !== 'completed')
    return model
  const display = pickObject(metadata, 'display')
  if (model.kind === 'read' && display?.type === 'directory' && Array.isArray(display.entries)) {
    const entries = display.entries.filter((entry): entry is string => typeof entry === 'string' && entry !== '')
    const totalEntries = pickNumber(display, 'totalEntries', undefined)
    const offset = pickNumber(display, 'offset', undefined)
    return {
      ...model,
      kind: 'list',
      input: { ...model.input, filePath: pickString(display, 'path') || pickString(model.input, 'filePath') },
      output: entries.join('\n'),
      body: { type: 'directory', source: {
        entries: entries.map(path => ({ path })),
        totalEntries: Number.isSafeInteger(totalEntries) && totalEntries! >= 0 ? totalEntries : undefined,
        offset: Number.isSafeInteger(offset) && offset! > 0 ? offset : undefined,
        truncated: pickBoolean(display, 'truncated') ?? false,
      } },
    }
  }
  if (model.kind === 'read' && display?.type === 'file' && typeof display.text === 'string') {
    const filePath = pickString(display, 'path') || pickString(model.input, 'filePath')
    const startLine = pickNumber(display, 'lineStart', undefined)
    return {
      ...model,
      output: display.text,
      input: { ...model.input, filePath },
      body: {
        type: 'read',
        source: readFileSourceFromContent({
          filePath,
          content: display.text,
          startLine,
          totalLines: pickNumber(display, 'totalLines', undefined),
        }),
      },
    }
  }
  if (model.kind === 'edit' || model.kind === 'write') {
    if (Array.isArray(metadata?.files)) {
      const sources = metadata.files.flatMap((entry) => {
        if (!isObject(entry))
          return []
        const oldPath = pickString(entry, 'filePath')
        const movePath = pickString(entry, 'movePath')
        const filePath = movePath || oldPath
        if (!filePath)
          return []
        const parsed = parseUnifiedDiffCached(pickString(entry, 'patch'))
        const source = parsed ? fileEditDiffFromHunks(filePath, parsed.hunks) : { filePath, structuredPatch: null, oldStr: '', newStr: '' }
        const operation = entry.type === 'add' ? 'add' as const : entry.type === 'delete' ? 'delete' as const : movePath ? 'move' as const : 'edit' as const
        if (!fileEditHasDiff(source) && operation === 'edit')
          return []
        return [{ ...source, operation, previousPath: movePath ? oldPath : undefined }]
      })
      if (sources.length > 0)
        return { ...model, body: { type: 'diff', sources } }
    }
    const diff = parseUnifiedDiffCached(pickString(metadata, 'diff'))
    const filePath = pickString(pickObject(metadata, 'filediff'), 'file') || pickString(model.input, 'filePath')
    const source = diff ? fileEditDiffFromHunks(filePath, diff.hunks) : null
    if (fileEditHasDiff(source))
      return { ...model, body: { type: 'diff', sources: [source] } }
  }
  if (['search', 'glob', 'grep'].includes(model.kind)) {
    const matches = pickNumber(metadata, 'matches', undefined)
    const count = pickNumber(metadata, 'count', undefined)
    const lines = matches !== undefined ? openCodeSearchLines(model.output, matches, pickBoolean(metadata, 'truncated') ?? false) : null
    if (lines) {
      return {
        ...model,
        kind: 'grep',
        body: { type: 'search', source: {
          variant: 'grep',
          filenames: [],
          content: '',
          lines,
          numFiles: new Set(lines.map(line => line.filePath)).size,
          numLines: lines.length,
          truncated: pickBoolean(metadata, 'truncated') ?? false,
          fallbackContent: '',
        } },
      }
    }
    if (matches !== undefined || count !== undefined) {
      const filenames = count !== undefined && count > 0 ? model.output.trim().split('\n') : []
      return {
        ...model,
        kind: count !== undefined ? 'glob' : 'grep',
        body: {
          type: 'search',
          source: {
            variant: count !== undefined ? 'glob' : 'search',
            filenames,
            content: count !== undefined ? '' : model.output,
            numFiles: count ?? 0,
            numLines: 0,
            matches,
            truncated: pickBoolean(metadata, 'truncated') ?? false,
            fallbackContent: model.output,
          },
        },
      }
    }
  }
  return model
}
