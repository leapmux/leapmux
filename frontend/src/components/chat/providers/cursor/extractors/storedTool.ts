import type { McpContentItem } from '../../../model/mcpToolCall'
import type { ToolCallSpec } from '../../../model/toolCall'
import type { FileChangeRequest } from '../../../model/tools/fileChange'
import type { ACPToolFacts } from '../../acp/extractors/toolCall'
import type { ACPToolSupplement } from '../../acp/toolSupplement'
import { ACP_SUPPLEMENT } from '~/generated/contracts/acp-protocol'
import { CURSOR_BLOCK_TYPE, CURSOR_STORED_BLOCK, CURSOR_STORED_TOOL } from '~/generated/contracts/cursor-protocol'
import { prettifyJson } from '~/lib/jsonFormat'
import { isObject, pickBoolean, pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { isAbsolute, join } from '~/lib/paths'
import { parseUnifiedDiffCached } from '../../../diff'
import { fileEditDiffFromHunks, fileEditHasDiff } from '../../../model/fileEditDiff'
import { mcpToolCallRequest, parseMcpContentItem } from '../../../model/mcpToolCall'
import { readFileResultFromContent } from '../../../model/readFileResult'
import { failedResult } from '../../../model/toolCall'
import { acpSupplementRawOutput } from '../../acp/toolSupplement'
import { DEFAULT_TOOL_REQUESTS } from '../../defaultToolRequests'
import { cursorAgentCall } from './agent'

function fileInWorkspace(workspace: string, path: string): string {
  return isAbsolute(path) ? path : join([workspace, path.replace(/^\.[/\\]/, '')])
}

/** What one row's saved tool result restored, beside the payload it may have fully built. */
export interface CursorStoredRestore {
  /** The arguments the saved record states, merged over the frame's own. */
  args: Record<string, unknown>
  /** The tool the saved record identifies, which the protocol frame does not. */
  name: string | undefined
  /** The saved record's own output, which outranks the frame's collected text. */
  output: string
  /** The whole payload a recognized record built, or null to keep building. */
  payload: ToolCallSpec | null
}

/** Cursor stores both standard content blocks and native protobuf content variants. */
function cursorMcpContent(saved: Record<string, unknown>, success: Record<string, unknown> | null, output: string): McpContentItem[] {
  const standard = Array.isArray(saved.experimental_content) ? saved.experimental_content : []
  const native = Array.isArray(success?.content) ? success.content : []
  const blocks = standard.length
    ? standard
    : native.map((entry) => {
        if (!isObject(entry))
          return entry
        const text = pickObject(entry, 'text')
        if (text)
          return { ...text, type: 'text' }
        const image = pickObject(entry, 'image')
        return image ? { ...image, type: 'image' } : entry
      })
  if (!blocks.length)
    return output ? [{ type: 'text', text: output }] : []
  const descriptions = pickObject(pickObject(pickObject(saved, CURSOR_STORED_TOOL.ProviderOptions), 'cursor'), 'imageDescriptions')
  return blocks.map((block, index) => {
    const item = parseMcpContentItem(block)
    const description = pickString(descriptions, String(index))
    return item.type === 'image' && description ? { ...item, source: { ...item.source, description } } : item
  })
}

function cursorSearchSource(success: Record<string, unknown>, output: string, glob: boolean) {
  const filenames: string[] = []
  const lines: Array<{ filePath: string, lineNumber?: number, text: string }> = []
  const workspaces = pickObject(success, 'workspaceResults') ?? {}
  let numMatches = 0
  let truncated = success.clientTruncated === true
  for (const [workspace, raw] of Object.entries(workspaces)) {
    if (!isObject(raw))
      continue
    const content = pickObject(raw, 'content')
    if (Array.isArray(content?.matches)) {
      for (const match of content.matches) {
        if (!isObject(match))
          continue
        const path = fileInWorkspace(workspace, pickString(match, 'file'))
        if (Array.isArray(match.matches)) {
          for (const line of match.matches) {
            if (!isObject(line) || typeof line.content !== 'string')
              continue
            const number = pickNumber(line, 'lineNumber', undefined)
            lines.push({ filePath: path, ...(number !== undefined ? { lineNumber: number } : {}), text: line.content })
            numMatches++
          }
        }
        filenames.push(path)
      }
      truncated ||= content.ripgrepTruncated === true || content.clientTruncated === true
    }
    const files = pickObject(raw, 'files')
    if (Array.isArray(files?.files)) {
      filenames.push(...files.files.filter((file): file is string => typeof file === 'string').map(file => fileInWorkspace(workspace, file)))
      truncated ||= files.ripgrepTruncated === true || files.clientTruncated === true
    }
  }
  if (Array.isArray(success.files)) {
    const path = pickString(success, 'path')
    filenames.push(...success.files.filter((file): file is string => typeof file === 'string').map(file => fileInWorkspace(path, file)))
  }
  const uniqueFiles = [...new Set(filenames)]
  return {
    filenames: glob || lines.length === 0 ? uniqueFiles : [],
    // NO second spelling of the matches. `searchResultText` builds this exact string
    // from `lines`, relativized, and reads `content` only when `lines` is empty --
    // where this join answered `''` anyway. The copy was dead on both branches.
    content: '',
    lines,
    numFiles: uniqueFiles.length,
    numLines: numMatches,
    truncated,
    fallbackContent: output,
    // POSITIVE EVIDENCE only: no transcript in `testdata/` records the wording
    // Cursor prints for a search that matched nothing, so an empty body is the one
    // empty result this build can recognize. Tighten the predicate once a real
    // transcript states that wording -- do not guess one.
    empty: output.trim() === '',
  }
}

/**
 * The words a call that ended WITHOUT an answer states: the reason it gave, then
 * whatever partial output the record still holds.
 *
 * BOTH, because the two say different things. The reason alone drops output the server
 * did produce, and the partial output alone reads as a call that succeeded.
 */
function failureText(reason: string, output: string): string {
  return reason && output && reason !== output ? `${reason}\n\n${output}` : reason || output
}

/**
 * The edit-family request a saved record states, from the SHARED table.
 *
 * The change a removal asks for draws no diff -- there is nothing to diff -- and this
 * reading kept a change only where one drew. The request then stated no file, which
 * `createToolCall` refuses (invariant I7): the row composes its header from that list at
 * every state of the call, so the removal came back as the uncategorized card.
 *
 * `path` is the file the RECORD names, which outranks the arguments for the same
 * reason the result's own change does: Cursor states the resolved path there and the
 * call's arguments may spell a relative one.
 */
function storedEditRequest(kind: 'edit' | 'write' | 'delete', args: Record<string, unknown>, path: string): FileChangeRequest {
  return DEFAULT_TOOL_REQUESTS[kind](path ? { ...args, path } : args)
}

/**
 * The record the worker built from Cursor's own transcript, under the shared ACP
 * `rawOutput` key.
 *
 * This is the browser's half of `cursorStoredToolOutput` in the worker: the three key
 * names are contract constants (contracts/cursor-protocol.json) and the Go tags are
 * pinned to the same table by TestSupplementTagsMatchTheContract. `providerOptions`
 * stays an open record, because what Cursor nests inside it is read here alone.
 */
export interface CursorStoredToolOutput {
  content: unknown[]
  toolArguments?: Record<string, unknown>
  providerOptions?: Record<string, unknown>
}

/** The stored record, or null when this row carries none the browser can read. */
export function cursorStoredToolOutput(supplement: ACPToolSupplement | undefined): CursorStoredToolOutput | null {
  const raw = acpSupplementRawOutput(supplement)
  const content = raw?.[CURSOR_STORED_TOOL.Content]
  if (!Array.isArray(content))
    return null
  const toolArguments = pickObject(raw, CURSOR_STORED_TOOL.ToolArguments, undefined)
  const providerOptions = pickObject(raw, CURSOR_STORED_TOOL.ProviderOptions, undefined)
  return {
    content,
    ...(toolArguments !== undefined ? { toolArguments } : {}),
    ...(providerOptions !== undefined ? { providerOptions } : {}),
  }
}

/**
 * Read the saved tool result whose ID matches the current ACP tool call.
 *
 * The saved record identifies the tool that ran and often carries the complete payload
 * beside the protocol's fragment, so a recognized record builds the whole call here
 * and the adapter returns it outright. An unrecognized one still restores the name,
 * the merged arguments and the output, which every later branch reads.
 */
export function cursorStoredRestore(facts: ACPToolFacts): CursorStoredRestore | null {
  const tool = facts.tool
  const raw = cursorStoredToolOutput(facts.extra)
  if (!raw || !tool.toolCallId)
    return null
  const saved = raw.content.find(value => isObject(value)
    && value[CURSOR_STORED_BLOCK.Type] === CURSOR_BLOCK_TYPE.ToolResult
    && value[CURSOR_STORED_BLOCK.ToolCallID] === tool.toolCallId)
  if (!isObject(saved))
    return null
  const native = pickObject(pickObject(raw.providerOptions, 'cursor'), 'highLevelToolCallResult')
  const success = pickObject(pickObject(native, 'output'), 'success')
  const name = pickString(saved, CURSOR_STORED_BLOCK.ToolName)
  const output = typeof saved.result === 'string' ? saved.result : saved.result !== undefined ? prettifyJson(saved.result) : facts.text
  const toolArguments = raw.toolArguments
  const args = { ...facts.args, ...toolArguments }
  const restore: CursorStoredRestore = { args, name: name || undefined, output, payload: null }

  // A launch states its own outcome: `cursorAgentCall` reads the frame's status and
  // the native error, and reports the run as failed or stopped with the reason as its
  // body. It therefore sits ABOVE the guard below on purpose.
  if (name === 'Task')
    return { ...restore, payload: cursorAgentCall(facts, args, native, output) }
  if (name.startsWith('mcp_')) {
    const server = pickString(args, 'providerIdentifier')
    const toolName = pickString(args, 'toolName') || name
    // The lifecycle the shared ACP ladder applies to every kind it builds, stated here
    // because this branch answers before that ladder runs. Cursor's own `isError`
    // stands beside the status word, because Cursor reports a server failure there. A
    // call that FAILED saved nothing the card can draw. Without this test the row drew
    // that record as a normal successful card, and the reason reached nobody.
    //
    // The failure WORD, never `status !== 'completed'`: a row the turn retained still
    // reports `in_progress` in its own frame, and reading that as a failure throws
    // away the answer the call did produce. A call the reader STOPPED keeps the
    // content it saved for the same reason -- the part of the answer that arrived is
    // what they asked to see.
    const unanswered = facts.status === 'failed' || native?.isError === true
    const result = unanswered ? failedResult(failureText(facts.text, output)) : { content: cursorMcpContent(saved, success, output) }
    return {
      ...restore,
      payload: {
        ...mcpToolCallRequest(server, toolName, toolArguments ?? pickObject(args, 'args') ?? {}),
        name,
        ...(facts.finished ? { result } : {}),
      },
    }
  }
  // The precondition of the four branches below, which all read `success`: a payload
  // is restored from a COMPLETED call's native record alone. The MCP branch above and
  // the launch above that both answer an unfinished or failed call themselves, which
  // is why each sits ahead of this.
  if (tool.status !== 'completed' || native?.isError === true || !success)
    return restore

  if (name === 'Read' && typeof success.content === 'string') {
    const path = pickString(success, 'path') || pickString(args, 'path') || ''
    const range = pickObject(success, 'readRange')
    const startLine = pickNumber(range, 'startLine', undefined)
    // Cursor reports a REFUSED read as a successful call: `content` is the empty
    // string and `exceededLimit` is set, while its own sentence about why sits in
    // the stored tool result. Taking `content` alone loses that sentence, and the
    // row then states the file is empty -- of a file it never read (RL-037).
    const refused = !success.content && !!output
    const fallbackContent = refused ? output : undefined
    const source = readFileResultFromContent({
      content: success.content,
      ...(startLine !== undefined ? { startLine } : {}),
      // The reason reaches the reader where the file would have been. A read that
      // returned nothing AND said nothing keeps the shared empty notice.
      ...(fallbackContent !== undefined ? { fallbackContent } : {}),
    })
    return {
      ...restore,
      payload: {
        kind: 'read',
        request: { path },
        ...(facts.finished ? { result: source } : {}),
      },
    }
  }
  if (name === 'StrReplace' || name === 'Write' || name === 'Delete') {
    // Each of the three states its OWN kind. Folding the removal into `edit` drew a
    // deleted file under the pencil and the word "Edit"; `deleteRenderer` exists, and
    // `DeleteRequest` and `DeleteResult` are the same two shapes the edit pair uses.
    const kind = name === 'Write' ? 'write' as const : name === 'Delete' ? 'delete' as const : 'edit' as const
    const path = pickString(success, 'path') || pickString(args, 'path') || ''
    const patch = parseUnifiedDiffCached(pickString(success, 'diffString'))
    const source = patch
      ? fileEditDiffFromHunks(path, patch.hunks)
      : typeof success.afterFullFileContent === 'string'
        ? { filePath: path, structuredPatch: null, oldStr: pickString(success, 'beforeFullFileContent'), newStr: success.afterFullFileContent }
        : null
    if (fileEditHasDiff(source)) {
      const originalFile = pickString(success, 'beforeFullFileContent', undefined)
      return {
        ...restore,
        payload: {
          kind,
          request: storedEditRequest(kind, args, path),
          result: { changes: [originalFile !== undefined ? { ...source, originalFile } : source] },
        },
      }
    }
    return restore
  }
  if (name === 'Grep' || name === 'Glob') {
    const kind = name === 'Glob' ? 'glob' as const : 'grep' as const
    return {
      ...restore,
      payload: {
        kind,
        label: name,
        request: {
          pattern: pickString(args, 'pattern') || pickString(args, 'glob_pattern') || '',
          paths: [pickString(args, 'path') || pickString(args, 'target_directory') || ''].filter(Boolean),
        },
        ...(facts.finished ? { result: cursorSearchSource(success, output, name === 'Glob') } : {}),
      },
    }
  }
  if (name === 'Shell') {
    const protocolOutput = pickObject(tool, ACP_SUPPLEMENT.RawOutput)
    const stdout = pickString(success, 'stdout') || pickString(protocolOutput, 'stdout')
    const stderr = pickString(success, 'stderr') || pickString(protocolOutput, 'stderr')
    const text = pickString(success, 'interleavedOutput') || [stdout, stderr].filter(Boolean).join(stdout.endsWith('\n') ? '' : '\n')
    const exitCode = pickNumber(protocolOutput, 'exitCode', undefined) ?? pickNumber(success, 'exitCode', undefined)
    const durationMs = pickNumber(success, 'localExecutionTimeMs', undefined) ?? pickNumber(success, 'executionTime', undefined)
    const description = pickString(args, 'description')
    return {
      ...restore,
      payload: {
        kind: 'execute',
        request: { command: pickString(args, 'command') || '', ...(description ? { description } : {}) },
        title: pickString(args, 'description'),
        ...(facts.finished
          ? { result: {
              commands: [{
                output: text,
                ...(exitCode !== undefined ? { exitCode } : {}),
                ...(durationMs !== undefined ? { durationMs } : {}),
                truncated: pickBoolean(success, 'outputTruncated') ?? false,
              }],
              unresolvedTerminals: [],
            } }
          : {}),
      },
    }
  }
  return restore
}
