import type { QuestionIR } from '../../../ir/questionBody'
import type { ToolCallPayload, ToolCallPayloadIR } from '../../../ir/toolCall'
import type { ToolKind } from '../../../ir/toolKind'
import type { FileChangeRequest } from '../../../ir/tools/fileChange'
import type { ACPToolCallAdapter, ACPToolFacts } from '../../acp/extractors/toolCall'
import { ACP_SUPPLEMENT } from '~/generated/contracts/acp-protocol'
import { isObject, pickBoolean, pickFirstString, pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { rawTodosToItems } from '~/models/todo'
import { chartResultFromSpec } from '../../../ir/chartResult'
import { fileEditDiffFromUnifiedPatch, fileEditDiffsFromChanges, fileEditHasDiff } from '../../../ir/fileEditDiff'
import { readFileResultFromContent } from '../../../ir/readFileResult'
import { failedResult, unparsedResult } from '../../../ir/toolCall'
import { acpFileEditFromToolCallContent } from '../../acp/extractors/fileEdit'
import { acpReadFromToolCall } from '../../acp/extractors/read'
import { acpPayloadFor } from '../../acp/extractors/toolCall'
import { questionsFromRecords } from '../../questionRecords'
import { TOOL_FILE_PATH_KEYS, TOOL_NEW_TEXT_KEYS, TOOL_OLD_TEXT_KEYS, toolInputPaths } from '../../toolInputKeys'
import { openCodeTaskResult } from '../extractors/agent'
import { openCodeSearchLines } from '../extractors/search'
import { OPENCODE_TOOL_NAMES } from '../toolNames'

/**
 * The kind a provider of this family knows that its own protocol layer does not state.
 *
 * Undefined keeps the frame's kind. Kilo answers `other` for every tool it adds on
 * top of the OpenCode set, and only Kilo knows what those tools do -- see
 * `kilo/toolKinds.ts`. OpenCode supplies none, so its rows are unchanged.
 */
export type OpenCodeFamilyToolKinds = (toolName: string) => ToolKind | undefined

/**
 * OpenCode and Kilo preserve display metadata alongside model-facing output.
 *
 * A FACTORY rather than one shared value, because the two daemons run different tool
 * sets behind the same wire format. `extraKinds` is the only difference, and it applies
 * where the frame states nothing useful.
 */
export function openCodeToolCallAdapterFor(extraKinds?: OpenCodeFamilyToolKinds): ACPToolCallAdapter {
  return facts => openCodeToolCall(facts, extraKinds)
}

/** A registry id the daemons send as the call title: one lowercase word. */
const REGISTRY_ID = /^[a-z0-9_-]+$/

/** Each question the call asked, or an empty list for a call that asked none. */
function openCodeQuestions(args: Record<string, unknown>): QuestionIR[] {
  return questionsFromRecords(
    args.questions,
    (question) => {
      const header = pickString(question, 'header')
      return { ...(header ? { header } : {}), question: pickString(question, 'question') }
    },
    (option) => {
      // The daemons send an option with a description and no label, and the sentence
      // is then the only word the reader has to click. It moves into the label rather
      // than staying beside an empty one, which leaves the option with no second line.
      const label = pickString(option, 'label')
      const description = pickString(option, 'description')
      if (!label && !description)
        return null
      return { label: label || description, ...(label && description ? { description } : {}) }
    },
  )
}

/** The provider's own reading of one call, beside the shared build. */
function openCodeToolCall(facts: ACPToolFacts, extraKinds: OpenCodeFamilyToolKinds | undefined): ToolCallPayloadIR {
  const tool = facts.tool
  const args = facts.args
  // The tool's REGISTRY ID, which OpenCode and Kilo send as the call title. The
  // protocol itself carries no tool name, so the shared build had only the kind
  // to identify the row with.
  const toolName = pickString(tool, 'title')
  const callKind = openCodeCallKind(facts, toolName, extraKinds)
  const kind = callKind.kind
  const metadata = pickObject(pickObject(tool, ACP_SUPPLEMENT.RawOutput), 'metadata')
  const task = openCodeTaskResult(facts.text, metadata, args)
  // The registry id IS the call's name: the protocol itself carries none, and the
  // wire kind identifies nothing the agent ran.
  const named = toolName ? { name: toolName } : {}

  // A task launch is an agent call: the instruction it was asked to run.
  if (toolName === OPENCODE_TOOL_NAMES.TASK || (kind === 'think' && pickString(args, 'subagent_type')) || (task && pickString(metadata, 'sessionId'))) {
    const agentType = pickString(args, 'subagent_type')
    const request = { description: pickString(args, 'description'), ...(agentType ? { agentType } : {}), prompt: pickString(args, 'prompt') }
    // The shared ladder answers whenever the run wrote no `<task>` wrapper, which a
    // launch that FAILED never writes: `openCodeTaskResult` reads that exact wrapper
    // and answers null for everything else. Without it the row drew its title and the
    // Error header with nothing at all between them, where every sibling states a
    // reason.
    const result = facts.finished && task ? { agents: [task] } : acpPayloadFor(facts, 'agent').result
    return {
      ...named,
      kind: 'agent',
      label: 'Task',
      title: request.description || 'Task',
      request,
      ...(result !== undefined ? { result } : {}),
    }
  }

  // Kilo's `chart` answers with the Chart.js configuration it normalized, and its own
  // description says "the chart is the response". The row draws that configuration;
  // the raw JSON below it would be the response the tool told the model not to give.
  if (kind === 'chart') {
    const title = pickString(metadata, 'title') || pickString(args, 'title')
    const description = pickString(metadata, 'description') || pickString(args, 'description')
    const meta = { ...(title ? { title } : {}), ...(description ? { description } : {}) }
    const spec = facts.text || pickString(args, 'spec') || '{}'
    // The lifecycle, in the order every other kind takes it. A result attached
    // unconditionally drew the red "not readable JSON" notice for the whole run --
    // and suppressed the live output tail, which `ToolMessage` shows only while the
    // result is absent -- and it replaced a failed chart's own reason with that
    // notice.
    //
    // A call the reader STOPPED keeps what the builder read, which is the rule the
    // shared ladder holds for every kind: the configuration that arrived is what they
    // asked to see, and the request's own `spec` stands for a call that printed
    // nothing.
    const chart = { ...named, kind: 'chart' as const, label: 'Chart', request: { spec, ...meta } }
    if (!facts.finished)
      return { ...chart, title: meta.title }
    if (facts.status === 'failed')
      return { ...chart, title: meta.title, result: failedResult(facts.text) }
    const source = chartResultFromSpec(spec, meta)
    return { ...chart, title: source.title || undefined, result: source }
  }

  if (toolName === OPENCODE_TOOL_NAMES.QUESTION) {
    const questions = openCodeQuestions(args)
    const header = questions[0]?.header || questions[0]?.question || 'Question'
    const asked = {
      ...named,
      kind: 'question' as const,
      label: 'Question',
      title: questions[0] ? header : undefined,
      request: { questions },
    }
    // The lifecycle, in the order every other kind takes it. The words this row
    // carries are the ANSWER, so the two states below cannot state them: a call that
    // has not finished has no answer yet, and a call that FAILED never asked, so the
    // reason it gave would draw as a choice that somebody made.
    if (!facts.finished)
      return asked
    if (facts.status === 'failed')
      return { ...asked, result: failedResult(facts.text) }
    return { ...asked, ...(facts.text ? { result: { answers: [{ header, answer: facts.text }] } } : {}) }
  }

  const rawTodos = Array.isArray(metadata?.todos) ? metadata.todos : args.todos
  if (toolName === OPENCODE_TOOL_NAMES.TODO_WRITE || Array.isArray(metadata?.todos)) {
    // `cancelled` reads as `deleted` inside normalizeTodoStatus, which every
    // provider's list goes through. OpenCode remapped it here first, and the copy
    // stayed correct only while no other provider sent the word -- Cursor does.
    const items = rawTodosToItems(rawTodos)
    // No title: `todoRenderer` composes the same words from the request this payload
    // carries, and a copy here is a second place for the wording to drift. Stated as
    // an EXPLICIT undefined, because `acpToolCallIR` spreads the adapter's payload
    // over its own frame-title default -- a payload that merely OMITTED the key let
    // that default ('todowrite') stand where every sibling provider composes '1 task'.
    const todo = { ...named, kind: 'todo' as const, title: undefined, request: { items } }
    // The lifecycle, in the order every other kind takes it. A call that has not
    // answered carries no result: `ToolMessage` draws the live output tail only while
    // the result is absent, and `todoRenderer` draws the REQUESTED list only there
    // too -- so a result on the opening frame claims a saved list the tool never
    // wrote.
    if (!facts.finished)
      return todo
    // A call that FAILED saved nothing, so the reason it gave is the answer. A call
    // the reader STOPPED keeps the list it collected, which the row marks as partial
    // from its own status.
    if (facts.status === 'failed')
      return { ...todo, result: failedResult(facts.text) }
    // A finished call whose frame carried no list at all: the words it printed are
    // the only answer it stated, and an empty checklist would claim it cleared the
    // list.
    if (!Array.isArray(rawTodos))
      return { ...todo, result: unparsedResult(facts.text) }
    return { ...todo, result: { items } }
  }

  // The protocol's own kinds, reading the display metadata the daemons keep
  // beside the output.
  const decorated = basePayload(facts, callKind, metadata)
  if (decorated)
    return { ...named, ...decorated }
  if (kind === '' || kind === 'other')
    return { ...named, ...genericPayload(facts, toolName) }
  // A kind the protocol states and this family decorates none of: the shared
  // build answers it. A command row whose title repeats the registry id states
  // nothing the command below it does not already say.
  const shared = acpPayloadFor(facts, kind)
  // A command row whose title repeats the registry id, the wire kind or the command
  // itself states nothing the command below it does not already say. A title with
  // spaces is a sentence the daemon wrote, and it stays.
  const command = pickString(args, 'command')
  const frameTitle = pickString(tool, 'title')
  const repeats = command && (frameTitle === command || (frameTitle === toolName && REGISTRY_ID.test(toolName)) || frameTitle === facts.wireKind)
  return { ...named, ...shared, ...(kind === 'execute' && repeats ? { title: undefined } : {}) }
}

/**
 * The kind of one call, and whether the ANSWER may still narrow it.
 *
 * `open` is true for the protocol's wide `search` alone, which states that a search ran
 * and never which one. Every other kind comes from the tool's own registry id, from the
 * frame's own word, or from the family table, and each of those is a statement the CALL
 * makes. A later frame must not contradict it -- see {@link openCodeCallKind}.
 */
interface OpenCodeCallKind {
  kind: ToolKind
  open: boolean
}

/**
 * The kind this family states for one call, after its own repairs.
 *
 * The kind is decided ONCE, from the call itself, and it holds for every state of that
 * call. The daemon states the kind on the opening frame and on a failed one, and it
 * omits the field entirely from a COMPLETED one -- so a kind the answer re-derived made
 * one call draw two different rows, and the failure ladder had to excuse the pair.
 */
function openCodeCallKind(facts: ACPToolFacts, toolName: string, extraKinds: OpenCodeFamilyToolKinds | undefined): OpenCodeCallKind {
  const args = facts.args
  // A search-kind call whose title spells glob or grep is that search.
  if (facts.wireKind === 'search' && (toolName === OPENCODE_TOOL_NAMES.GLOB || toolName === OPENCODE_TOOL_NAMES.GREP))
    return { kind: toolName === OPENCODE_TOOL_NAMES.GLOB ? 'glob' : 'grep', open: false }
  // An edit frame that carries whole-file content is a write.
  if (facts.wireKind === 'edit' && typeof args.content === 'string' && typeof args.filePath === 'string')
    return { kind: 'write', open: false }
  // ONLY where the frame said nothing useful. A provider table that overrode a real
  // kind would undo the repairs above, and would overwrite the kinds Kilo's own
  // protocol layer does state.
  if (facts.wireKind === 'other' || facts.wireKind === 'mcp' || facts.wireKind === '') {
    const extra = extraKinds?.(toolName)
    if (extra)
      return { kind: extra, open: false }
  }
  // The protocol's `search` is the one kind that leaves the question open: it says a
  // search ran, and the daemon spells a glob, a grep and a documentation lookup with
  // it. The counters the answer carries are the only evidence of which one, so
  // {@link basePayload} may narrow THIS kind and no other.
  return { kind: facts.wireKind, open: facts.wireKind === 'search' }
}

/**
 * The typed payload of one protocol kind, reading the daemon's display metadata.
 *
 * Every kind here walks the lifecycle the shared ACP ladder holds: no result while the
 * call still runs, the reason it stated when it FAILED, and the body this builder read
 * in every other case. A call the reader STOPPED takes that last path, because the
 * lines, the hits and the diff that did arrive are what they asked to see. The header
 * is unaffected: `toolRowStatusOutcome` composes it from the row's own status.
 *
 * The kind the call arrived with is the kind it keeps. Two branches below answer a
 * different one, and each states at the site why the answer carries a fact the call
 * itself could not: the directory listing a `read` turns out to be, and the narrowing of
 * the wide `search` that {@link OpenCodeCallKind.open} admits.
 */
function basePayload(facts: ACPToolFacts, callKind: OpenCodeCallKind, metadata: Record<string, unknown> | null): ToolCallPayloadIR | null {
  const { kind, open } = callKind
  const args = facts.args
  const display = pickObject(metadata, 'display')

  if (kind === 'read') {
    const offset = pickNumber(args, 'offset', undefined) ?? undefined
    const limit = pickNumber(args, 'limit', undefined) ?? undefined
    const request = { path: pickFirstString(args, TOOL_FILE_PATH_KEYS) ?? '', ...(offset !== undefined ? { offset } : {}), ...(limit !== undefined ? { limit } : {}) }
    if (!facts.finished)
      return { kind, request }
    if (facts.status === 'failed')
      return { kind, request, result: failedResult(facts.text) }
    // The ONE kind this family moves when the answer lands, and the move is not a
    // second reading of a fact the call already stated. OpenCode's `read` runs two
    // operations behind one registry id -- its own description opens "Read a file or
    // directory" -- and only the answer says which one ran. The two carry different
    // request AND result types, `ReadRequest`/`ReadFileResult` against
    // `ListRequest`/`ListResult`, so no result shape can hold the difference and leave
    // the kind alone. A call that is still in flight, and one that FAILED, therefore
    // draw the read they asked for; the row takes the List icon and label at the moment
    // the entries arrive. The virtual list is unaffected, because both draw the `tool`
    // row that `ROW_KIND_FOR_CATEGORY` measures.
    if (display?.type === 'directory' && Array.isArray(display.entries)) {
      const entries = display.entries.filter((entry): entry is string => typeof entry === 'string' && entry !== '')
      const totalEntries = pickNumber(display, 'totalEntries', undefined)
      const offset = pickNumber(display, 'offset', undefined)
      const total = totalEntries !== undefined && Number.isSafeInteger(totalEntries) && totalEntries >= 0 ? totalEntries : undefined
      const start = offset !== undefined && Number.isSafeInteger(offset) && offset > 0 ? offset : undefined
      return {
        kind: 'list',
        request: { path: pickString(display, 'path') || request.path || '.' },
        result: {
          entries: entries.map(path => ({ path })),
          ...(total !== undefined ? { totalEntries: total } : {}),
          ...(start !== undefined ? { offset: start } : {}),
          truncated: pickBoolean(display, 'truncated') ?? false,
        },
      }
    }
    if (display?.type === 'file' && typeof display.text === 'string') {
      const lineStart = pickNumber(display, 'lineStart', undefined)
      return {
        kind,
        request: { ...request, path: pickString(display, 'path') || request.path },
        result: readFileResultFromContent({ content: display.text, ...(lineStart !== undefined ? { startLine: lineStart } : {}), fallbackContent: display.text }),
      }
    }
    // A content text block is the file body: it parses as cat-n when the daemon
    // numbered the lines, and reads as plain text otherwise.
    const contentText = Array.isArray(facts.tool.content)
      ? facts.tool.content.flatMap((entry) => {
          const inner = pickObject(entry, 'content')
          // The VALUE, not `pickString`'s answer. `pickString` returns `''` for a
          // key that is absent or holds a number, so the test was true for every
          // block and the join swallowed each one as the empty string.
          return typeof inner?.text === 'string' ? [inner.text] : []
        }).join('')
      : ''
    if (contentText) {
      const source = acpReadFromToolCall(facts.tool)
      if (source?.lines !== null && source)
        return { kind, request, result: source }
      return { kind, request, images: facts.images, result: unparsedResult(contentText) }
    }
    // A COMPLETED read that printed nothing still carries a result (the builder's
    // I2): the unparsed brand states the empty answer while the pictures ride the
    // call. A CANCELLED one may state none, exactly as the shared ladder holds.
    // A CANCELLED read may state none, so the result rides only when one exists.
    return { kind, request, images: facts.images, ...(facts.text || facts.status === 'completed' ? { result: unparsedResult(facts.text) } : {}) }
  }

  if (kind === 'edit' || kind === 'write') {
    // EVERY alias of the three keys, from the one shared list. OpenCode's own tools
    // spell the file `filePath`, and the tools Kilo adds on top of them spell it
    // `path` -- `notebook_edit` does, with `old_string` and `new_string` beside it.
    // A change the reader cannot pair with a file leaves the row headed by the word
    // "Edit" and nothing else, at every state of the call.
    const filePath = pickFirstString(args, TOOL_FILE_PATH_KEYS) ?? ''
    const patchText = pickString(args, 'patch')
    const patchSource = patchText ? fileEditDiffFromUnifiedPatch(filePath, patchText) : null
    const changes = patchSource && fileEditHasDiff(patchSource)
      ? [patchSource]
      : filePath
        ? kind === 'write'
          ? [{ filePath, operation: 'add' as const, oldStr: '', newStr: pickString(args, 'content'), structuredPatch: null }]
          : [{ filePath, oldStr: pickFirstString(args, TOOL_OLD_TEXT_KEYS) ?? '', newStr: pickFirstString(args, TOOL_NEW_TEXT_KEYS) ?? '', structuredPatch: null }]
        : []
    const request: FileChangeRequest = { changes }
    if (!facts.finished)
      return { kind, request }
    if (facts.status === 'failed')
      return { kind, request, result: failedResult(facts.text) }
    if (Array.isArray(metadata?.files)) {
      const sources = fileEditDiffsFromChanges(metadata.files.flatMap((entry) => {
        if (!isObject(entry))
          return []
        const oldPath = pickString(entry, 'filePath')
        const movePath = pickString(entry, 'movePath')
        const target = movePath || oldPath
        if (!target)
          return []
        return [{
          filePath: target,
          ...(movePath ? { previousPath: oldPath } : {}),
          operation: entry.type === 'add' ? 'add' as const : entry.type === 'delete' ? 'delete' as const : movePath ? 'move' as const : 'edit' as const,
          patch: pickString(entry, 'patch'),
        }]
      }))
      if (sources.length > 0)
        return { kind, request, result: { changes: sources } }
    }
    const diffFile = pickString(pickObject(metadata, 'filediff'), 'file') || filePath
    const source = fileEditDiffFromUnifiedPatch(diffFile, pickString(metadata, 'diff'))
    if (fileEditHasDiff(source))
      return { kind, request, result: { changes: [source] } }
    // Last, a diff the call CONTENT carries: the metadata's own patch and file
    // list both describe the change better than the replacement fragment does.
    const contentDiff = Array.isArray(facts.tool.content)
      ? facts.tool.content.flatMap((entry) => {
          const fromContent = acpFileEditFromToolCallContent([entry])
          return fileEditHasDiff(fromContent) ? [fromContent] : []
        })
      : []
    if (contentDiff.length > 0)
      return { kind, request, result: { changes: contentDiff } }
    // The daemon said something in words, and those words may be the whole point
    // ("No file changes occurred."): they stay unparsed, and the request body
    // draws beside them.
    if (facts.text)
      return { kind, request, result: unparsedResult(facts.text) }
    // Nothing stated the landed change and the daemon said nothing, so the change
    // the call ASKED for is the best statement of what it applied. It claims
    // nothing beyond the call's own arguments.
    // Only a change that can DRAW. A path-only entry has no hunks and no old/new
    // text, so `fileEditHasDiff` is false for it: returning it as the RESULT made
    // `FileChangesBody` draw nothing while suppressing the request body that would
    // at least have stated the file, so a finished edit rendered as an empty card.
    // And only a call that COMPLETED. A call the reader stopped applied nothing, so
    // the same list drawn as the RESULT states a change that never reached the file
    // -- `FileChangesBody` draws whatever the result holds, where
    // `RequestedChangesBody` refuses the request of an interrupted call.
    if (facts.status === 'completed' && request.changes.some(fileEditHasDiff))
      return { kind, request, result: { changes: request.changes } }
    return { kind, request }
  }

  if (kind === 'glob' || kind === 'grep' || kind === 'search') {
    const request = { pattern: pickString(args, 'pattern') || pickString(args, 'query') || '', paths: toolInputPaths(args) }
    if (!facts.finished)
      return { kind, request }
    if (facts.status === 'failed')
      return { kind, request, result: failedResult(facts.text) }
    const matches = pickNumber(metadata, 'matches', undefined)
    const count = pickNumber(metadata, 'count', undefined)
    const lines = matches !== undefined ? openCodeSearchLines(facts.text, matches, pickBoolean(metadata, 'truncated') ?? false) : null
    if (lines) {
      return {
        // The call's own kind, unless it left the question open. Reaching here says
        // that the BODY reads as OpenCode's grep format, and that is not a statement
        // about which tool ran -- the registry id already made that one.
        kind: open ? 'grep' : kind,
        request,
        result: {
          filenames: [],
          content: '',
          lines,
          numFiles: new Set(lines.map(line => line.filePath)).size,
          numLines: lines.length,
          truncated: pickBoolean(metadata, 'truncated') ?? false,
          fallbackContent: '',
          // `openCodeSearchLines` answers a list only when it read EVERY row of
          // OpenCode's own format, and it recognizes that format's empty wording
          // itself. No row is therefore "the tool found nothing", not "this build
          // could not read the body".
          empty: lines.length === 0,
        },
      }
    }
    if (matches !== undefined || count !== undefined) {
      // The TEXT decides, with the count beside it. `''.trim().split('\n')` is
      // `['']`, so a daemon that reported a positive count and printed no body
      // listed one file with no name at all.
      const listed = facts.text.trim()
      const filenames = count !== undefined && count > 0 && listed ? listed.split('\n') : []
      return {
        // The KIND words the summary: a count is a glob, a match total is the ACP
        // search phrasing. That reading applies to a call that left the kind OPEN and
        // to no other, because the counters are the only evidence there. A call whose
        // registry id spells the tool keeps the kind that id gave it.
        kind: open ? (count !== undefined ? 'glob' : 'search') : kind,
        request,
        result: {
          filenames,
          content: count !== undefined ? '' : facts.text,
          numFiles: count ?? 0,
          numLines: 0,
          ...(matches !== undefined ? { matchCount: matches } : {}),
          truncated: pickBoolean(metadata, 'truncated') ?? false,
          fallbackContent: facts.text,
          // POSITIVE EVIDENCE only. This branch reads a COUNT the daemon stated and
          // never the body's own words, and no transcript in `testdata/` records
          // what OpenCode prints here when it matches nothing. An empty body is the
          // one empty result this build can recognize. Tighten the predicate once a
          // real transcript states that wording -- do not guess one.
          empty: facts.text.trim() === '',
        },
      }
    }
    return { kind, request, ...(facts.text ? { result: unparsedResult(facts.text) } : {}) }
  }

  // The kinds the protocol states but this family decorates none of: think, fetch,
  // and every one an `extraKinds` table states. The shared build answers them.
  return null
}

/**
 * The generic trio's payload for a call this family does not decorate.
 *
 * The shared build owns the card's content blocks, so its payload stands except
 * for the tool name: the registry id identifies the call, which the wire kind cannot.
 */
function genericPayload(facts: ACPToolFacts, toolName: string): ToolCallPayload<'mcp'> {
  const request = { server: '', tool: toolName || 'tool', args: facts.args }
  if (!facts.finished)
    return { kind: 'mcp', request }
  // The shared build at `mcp`, NOT the adapter's `base`. `base` answers the kind the
  // WIRE stated, and this branch runs when THIS family reclassified the call to the
  // generic trio -- so for a frame whose wire kind was `read`, spreading `base()` put
  // a file body behind an MCP card's type. Asking for the card directly states what
  // the row draws.
  return { ...acpPayloadFor(facts, 'mcp'), kind: 'mcp', request }
}
