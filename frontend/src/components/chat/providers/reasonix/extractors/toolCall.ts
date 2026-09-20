import type { SearchToolKind } from '../../../model/searchResult'
import type { ACPToolCallAdapter } from '../../acp/extractors/toolCall'
import { rawTodosToItems } from '~/components/chat/normalizers/todo'
import { ACP_SUPPLEMENT_REQUEST } from '~/generated/contracts/acp-protocol'
import { REASONIX_CAPABILITY_ACTION, REASONIX_CAPABILITY_PREFIX, REASONIX_TOOL, REASONIX_TOOL_RECORD } from '~/generated/contracts/reasonix-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { parseUnifiedDiffCached } from '../../../diff'
import { fileEditDiffFromHunks, fileEditHasDiff } from '../../../model/fileEditDiff'
import { mcpToolCallRequest, parseMcpContentItem, parseMcpToolName, splitPrefixedPair } from '../../../model/mcpToolCall'
import { failedResult, unparsedResult } from '../../../model/toolCall'
import { flattenAcpContent } from '../../acp/content'
import { acpRemapFacts, acpSpecFor } from '../../acp/extractors/toolCall'
import { acpSupplementRawOutput } from '../../acp/toolSupplement'
import { grepMatches } from '../../grepOutput'
import { reasonixAgentResult } from '../extractors/agent'
import { reasonixEditReceipt } from '../extractors/fileEdit'
import { reasonixDirectoryOutput } from '../extractors/list'
import { isReasonixTool, REASONIX_TOOL_KINDS, REASONIX_TOOL_NAME } from '../toolKinds'

/**
 * The capability prefix that carries a Model Context Protocol tool.
 *
 * Not in `contracts/reasonix-protocol.json`, which holds the identifiers BOTH programs
 * read. The worker never splits this one, so the contract rule keeps it on the side
 * that does.
 */
const REASONIX_MCP_CAPABILITY_PREFIX = 'mcp-tool:'

/** The search result one completed glob or grep states, from the words it printed. */
function reasonixSearchResult(name: Extract<SearchToolKind, 'glob' | 'grep'>, output: string) {
  const trimmed = output.trim()
  const empty = trimmed === '(no matches)' || trimmed === ''
  const lines = empty ? [] : trimmed.split('\n')
  const truncated = lines.some(line => /^\.\.\. \((?:truncated|timed out)/.test(line))
  if (name === 'glob') {
    const filenames = lines.filter(line => line !== '' && !/^\.\.\. \(/.test(line))
    return {
      filenames,
      content: '',
      numFiles: filenames.length,
      numLines: 0,
      truncated,
      fallbackContent: empty ? '' : output,
      empty,
    }
  }
  const matches = grepMatches(lines)
  const matchCount = matches.lines.length || (empty ? 0 : undefined)
  return {
    filenames: [],
    content: output,
    numFiles: matches.numFiles,
    numLines: matches.lines.length,
    ...(matchCount !== undefined ? { matchCount } : {}),
    truncated,
    fallbackContent: empty ? '' : output,
    empty,
  }
}

/** Unwrap a capability call before choosing the normal tool components. */
export const reasonixToolCallAdapter: ACPToolCallAdapter = (facts, _base) => {
  const tool = facts.tool
  // The worker wraps Reasonix's own transcript record under this envelope key, and
  // both sides read the record's field names from contracts/reasonix-protocol.json.
  // A Go struct tag takes a literal, so TestReasonixToolRecordTagsMatchTheContract
  // pins the worker's tags to the same table.
  const stored = pickObject(acpSupplementRawOutput(facts.extra), REASONIX_TOOL_RECORD.Envelope)
  const saved = stored?.[REASONIX_TOOL_RECORD.RoleField] === REASONIX_TOOL_RECORD.ToolRole && stored[REASONIX_TOOL_RECORD.ToolCallIDField] === tool.toolCallId ? stored : undefined
  const savedText = pickString(saved, REASONIX_TOOL_RECORD.RawContentField, undefined) || pickString(saved, REASONIX_TOOL_RECORD.ContentField, undefined)
  const readResult = pickObject(saved, 'read_result')
  const savedPath = pickString(pickObject(readResult, 'source'), 'canonical_path')
  const resolvedTool = savedText !== undefined
    ? { ...tool, content: [{ type: 'content', content: { type: 'text', text: savedText } }] }
    : tool
  const output = savedText ?? facts.text
  let name = pickString(tool, 'title')
  let input = facts.args
  let mcp: { server: string, tool: string } | undefined
  if (name === REASONIX_TOOL.UseCapability && input.action === REASONIX_CAPABILITY_ACTION.Call) {
    const id = pickString(input, 'capability_id')
    const args = pickObject(input, 'arguments')
    if (args && id.startsWith(REASONIX_CAPABILITY_PREFIX.Tool)) {
      name = id.slice(REASONIX_CAPABILITY_PREFIX.Tool.length)
      input = args
    }
    else if (args) {
      const pair = splitPrefixedPair(id, REASONIX_MCP_CAPABILITY_PREFIX, '/')
      if (pair) {
        mcp = pair
        input = args
      }
    }
  }
  else {
    mcp = parseMcpToolName(name) ?? undefined
  }
  if (mcp) {
    // The lifecycle the shared ladder applies to every kind it builds, stated here
    // because this branch answers before that ladder runs. A server call that FAILED
    // gave no answer. Its card is then not the body; the reason it gave is. Without
    // this test the row drew that (usually empty) card and `[no output]` where the
    // reason belongs.
    //
    // A call the reader STOPPED is not a failure, and it keeps the blocks that did
    // arrive: they are the part of the answer the reader asked to see. The header
    // still states `Interrupted`, which `toolCallStatusOutcome` composes from the
    // row's own status.
    const unanswered = facts.status === 'failed'
    return {
      ...mcpToolCallRequest(mcp.server, mcp.tool, input),
      name,
      ...(facts.finished ? { result: unanswered ? failedResult(output) : { content: flattenAcpContent(resolvedTool.content).map(parseMcpContentItem) } } : {}),
    }
  }
  name ||= pickString(saved, REASONIX_TOOL_RECORD.NameField)
  // The OUTCOME is not part of this test. A `todo_write` that failed is still a
  // checklist, and a status test here drops the row to the kind the WIRE states --
  // which Reasonix sends as `edit`, so a failed list draws as a file change that
  // states no file. `Array.isArray` stays: a call that carries no list cannot draw
  // one, and an empty checklist claims that the agent cleared the list.
  if (name === REASONIX_TOOL_NAME.TodoWrite && Array.isArray(input.todos)) {
    const items = rawTodosToItems(input.todos)
    return {
      kind: 'todo',
      name,
      // No title and a POPULATED request: `todoRenderer` composes the same words from
      // the request, and its own guard (`role !== 'result' && !hasResultRow`) is what
      // stops a double draw -- so emptying the list here only hid it while the call ran.
      // The absent title is stated as an EXPLICIT undefined because the ACP wrapper
      // spreads this payload over its own frame-title default, and a key that is merely
      // omitted lets that default stand where the renderer should compose the count.
      title: undefined,
      request: { items },
      // The lifecycle the shared ladder applies to every kind it builds. A call that
      // FAILED saved nothing, so the reason it gave is the answer. A call the reader
      // STOPPED keeps the list it collected, which the row marks as partial from its
      // own status.
      ...(facts.finished ? { result: facts.status === 'failed' ? failedResult(output) : { items } } : {}),
    }
  }
  if (name === REASONIX_TOOL_NAME.ReadFile && savedPath)
    input = { ...input, path: savedPath }
  // The unwrapped call, under Reasonix's OWN tool name rather than the capability
  // wrapper's: `use_capability` is the envelope, and `name` is what ran inside it.
  const kind = isReasonixTool(name) ? REASONIX_TOOL_KINDS[name] : facts.wireKind
  // RE-DERIVED, not cloned. A hand-written `{...facts, tool, args}` kept every fact
  // the shared build reads from the KIND or from the CONTENT at its pre-remap value:
  // the text stayed the text of the original frame, so a completed `read_file` whose
  // wire content is empty and whose recovered content is not cat-n fell back to that
  // stale empty string and drew no result body at all.
  const remapFacts = acpRemapFacts(facts, { tool: { ...resolvedTool, title: name, [ACP_SUPPLEMENT_REQUEST.RawInput]: input }, kind })
  // Each branch below asks for the payload at the kind the TABLE states for the name
  // it already matched, so the pair is checked rather than asserted: the shared build
  // at `REASONIX_TOOL_KINDS.glob` is a glob's payload, and a search result overrides its result
  // because that is what a glob declares. The single `kind` variable is the whole union
  // at the type level, so every override through it had to be cast -- and a table entry
  // changed under a branch still compiled.
  const remapped = acpSpecFor(remapFacts, kind)
  if (name === REASONIX_TOOL.Task || name === REASONIX_TOOL.ReadOnlyTask) {
    return {
      kind: 'agent',
      name,
      request: { description: pickString(input, 'description'), agentType: pickString(input, 'profile'), prompt: pickString(input, 'prompt') },
      // `facts.finished`, never `acpToolFinished(tool)`: the frame alone cannot see
      // the turn's own outcome, so a retained subagent row dropped its report.
      ...(facts.finished
        ? { result: { agents: [reasonixAgentResult({ toolName: name, input, output, originalOutput: facts.text, status: tool.status })] } }
        : {}),
    }
  }
  if (name === REASONIX_TOOL_NAME.Bash) {
    const base = acpSpecFor(remapFacts, REASONIX_TOOL_KINDS[name])
    return { ...base, name, title: pickString(input, 'description') || base.title }
  }
  if (name === REASONIX_TOOL_NAME.Ls) {
    // ONE branch for the two states, and no request of its own: the table already
    // states this tool's kind, so the shared build reads the path out of the same
    // arguments -- under `filePath` and `file_path` as well, which a hand-written
    // `input.path` missed. A second branch spelled the label a second time.
    const list = { ...acpSpecFor(remapFacts, REASONIX_TOOL_KINDS[name]), name, label: 'List Files' }
    return tool.status === 'completed' ? { ...list, result: reasonixDirectoryOutput(output) } : list
  }
  if (tool.status === 'completed' && (name === REASONIX_TOOL_NAME.DeleteRange || name === REASONIX_TOOL_NAME.DeleteSymbol)) {
    const patch = parseUnifiedDiffCached(output)
    // The shared build's REQUEST stands: it states the file the removal asked for, and
    // the row composes its header from that list at every state of the call. An empty
    // list here takes the file out of the header and leaves the word "Delete" alone.
    // `RequestedChangesBody` already keeps the two bodies apart, because it draws
    // nothing once a result exists.
    if (patch)
      return { ...acpSpecFor(remapFacts, REASONIX_TOOL_KINDS[name]), name, result: { changes: [fileEditDiffFromHunks(pickString(input, 'path'), patch.hunks)] } }
  }
  if (tool.status === 'completed' && (name === REASONIX_TOOL_NAME.EditFile || name === REASONIX_TOOL_NAME.MultiEdit)) {
    const path = pickString(input, 'path')
    const receipt = reasonixEditReceipt(output, path, input)
    const edit = acpSpecFor(remapFacts, REASONIX_TOOL_KINDS[name])
    if (receipt !== null) {
      // An empty receipt says the daemon reported what it DID without a diff: the
      // words stay, drawn as the answer rather than a change.
      return receipt.length
        ? { ...edit, name, result: { changes: receipt } }
        : { ...edit, name, result: unparsedResult(output) }
    }
    if (name === REASONIX_TOOL_NAME.MultiEdit && Array.isArray(input.edits)) {
      const changes = input.edits.flatMap(edit => isObject(edit) && typeof edit.old_string === 'string' && typeof edit.new_string === 'string'
        ? [{ filePath: path, structuredPatch: null, oldStr: edit.old_string, newStr: edit.new_string, showLineNumbers: false }]
        : []).filter(fileEditHasDiff)
      if (changes.length)
        return { ...edit, name, request: { changes }, result: unparsedResult(output) }
    }
  }
  if (name === REASONIX_TOOL_NAME.Glob || name === REASONIX_TOOL_NAME.Grep) {
    // The shared build's request stands: it reads the same `pattern` and, for the
    // paths, the whole alias list plus a native `paths` array. The narrower copy that
    // replaced it dropped every one of those.
    const search = { ...acpSpecFor(remapFacts, REASONIX_TOOL_KINDS[name]), name }
    return tool.status === 'completed' ? { ...search, result: reasonixSearchResult(name, output) } : search
  }
  return { ...remapped, ...(name ? { name } : {}) }
}
