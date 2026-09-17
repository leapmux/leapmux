import type { QuestionIR } from '../../../ir/questionBody'
import type { SearchBodyKind } from '../../../ir/searchResult'
import type { ToolCallPayloadIR } from '../../../ir/toolCall'
import type { ToolKind } from '../../../ir/toolKind'
import type { ACPToolCallAdapter, ACPToolFacts } from '../../acp/extractors/toolCall'
import { ACP_SUPPLEMENT, ACP_SUPPLEMENT_REQUEST } from '~/generated/contracts/acp-protocol'
import { CURSOR_TOOL } from '~/generated/contracts/cursor-protocol'
import { isObject, pickBoolean, pickFirstString, pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { mcpToolCallRequest } from '../../../ir/mcpToolCall'
import { readFileResultFromContent } from '../../../ir/readFileResult'
import { failedResult, proseResult, unparsedResult } from '../../../ir/toolCall'
import { acpBasePayload, acpPayloadFor, acpRemapFacts } from '../../acp/extractors/toolCall'
import { questionsFromRecords } from '../../questionRecords'
import { TOOL_FILE_PATH_KEYS } from '../../toolInputKeys'
import { cursorAgentCall } from '../extractors/agent'
import { cursorExtension, cursorGeneratedImages, cursorTaskDetails, cursorTodoItems } from '../extractors/extensions'
import { cursorStoredRestore } from '../extractors/storedTool'

/**
 * The tool Cursor writes a plan with.
 *
 * Not in `contracts/cursor-protocol.json`, which holds the identifiers BOTH programs
 * read. The worker knows a plan by its JSON-RPC method (`cursor/create_plan`) and never
 * reads this tool name, so the contract rule keeps it on the one side that does.
 */
const CURSOR_TOOL_CREATE_PLAN = 'createPlan'

/**
 * The other three tools that name themselves in `rawInput`.
 *
 * Frontend-only, for the same reason as the plan above: the worker knows each of
 * these by its JSON-RPC method (`cursor/update_todos` and its two siblings, which
 * ARE in the contract) and never reads the tool name.
 */
const CURSOR_TOOL_ASK_QUESTION = 'askQuestion'
const CURSOR_TOOL_UPDATE_TODOS = 'updateTodos'
const CURSOR_TOOL_GENERATE_IMAGE = 'generateImage'

/**
 * The three kinds that draw a search body.
 *
 * A type GUARD rather than a set membership test: `Set.has` answers a boolean and
 * narrows nothing, so the payload built under it was checked against every kind at
 * once and a result field that belonged to none still compiled.
 */
function isSearchKind(kind: ToolKind): kind is SearchBodyKind {
  return kind === 'search' || kind === 'glob' || kind === 'grep'
}

/**
 * The questions one `askQuestion` call asked.
 *
 * Cursor's own shape is `{title, questions:[{prompt, options:[{id,label}]}]}`; the
 * row is the transcript record of an interaction the reader answered in the control
 * banner, so it states the question and the choices that were offered.
 */
function cursorQuestions(input: Record<string, unknown>): QuestionIR[] {
  return questionsFromRecords(
    input.questions,
    (question) => {
      const header = pickString(question, 'header')
      return { ...(header ? { header } : {}), question: pickString(question, 'prompt') }
    },
    (option) => {
      // Cursor sends an option with an id and no label, and the id is the word the
      // runtime itself shows there.
      const label = pickString(option, 'label') || pickString(option, 'id')
      const description = pickString(option, 'description')
      return label ? { label, ...(description ? { description } : {}) } : null
    },
  )
}

/**
 * The search shape one call has, read before the build.
 *
 * Cursor states a search's shape in two places -- the title the runtime rendered,
 * and the counters the result carries -- and the two disagree for a file search
 * that also reports matches. The title is the stronger statement, so it leads.
 *
 * Precedence, strongest first:
 *  1. The rendered TITLE. The runtime writes `Find` for a file-name search and
 *     `grep` for a content search.
 *  2. The result COUNTERS. `totalFiles` rides a file-name search, and a content
 *     search reports its match total instead.
 *
 * The title match must accept every title the runtime composes. Cursor builds the
 * content-search title from the arguments: `grep`, then one optional flag for each
 * argument (`-i`, `-n`, `-A N`, `-l`, `--include="..."`, and more), then the quoted
 * pattern last. A flag moves the pattern away from the front, so the `grep ` prefix
 * is the one part that stays constant. The file-name search writes `Find`, then an
 * optional path and an optional pattern, each one inside backticks.
 */
export function cursorSearchKind(tool: Record<string, unknown>, raw: Record<string, unknown> | null): SearchBodyKind {
  const title = pickString(tool, 'title')
  if (title === 'Find' || title.startsWith('Find `'))
    return 'glob'
  if (title === 'grep' || title.startsWith('grep '))
    return 'grep'
  if (tool.status !== 'completed' || !raw)
    return 'search'
  const totalFiles = pickNumber(raw, 'totalFiles', undefined)
  const matches = pickNumber(raw, 'totalMatches', undefined) ?? pickNumber(raw, 'resultCount', undefined)
  if (totalFiles === undefined && matches === undefined)
    return 'search'
  return totalFiles !== undefined ? 'glob' : 'grep'
}

/**
 * One Cursor call, before the two post-conditions every row carries.
 *
 * A saved tool result that identifies its tool builds the whole call outright; the
 * five tools that state their own name in `rawInput` follow; everything else reads
 * the protocol frame through the shared build, with the search shape repaired first.
 */
function cursorToolCall(source: ACPToolFacts): ToolCallPayloadIR {
  const stored = cursorStoredRestore(source)
  // The saved record's own output OUTRANKS the frame's collected text, which is what
  // `CursorStoredRestore.output` declares. Cursor answers in `rawOutput` and sends no
  // Agent Client Protocol content block, so the frame's text is empty for most rows --
  // and a record this build does not recognize, or a call that failed, then had
  // nothing at all to state.
  const facts = stored?.output && stored.output !== source.text ? { ...source, text: stored.output } : source
  const base = () => acpBasePayload(facts)
  const tool = facts.tool
  const raw = pickObject(tool, ACP_SUPPLEMENT.RawOutput)
  const input = stored?.args ?? facts.args
  // Cursor's protocol carries no tool name, but five tools state their own name
  // inside `rawInput`, and a saved record identifies every tool it restored. That
  // name is what the coverage table and the MCP card key on.
  const named = pickString(input, '_toolName')
  const name = named || stored?.name
  // `toolCall` folds an absent name onto the envelope's own, so the key rides only
  // when one was read.
  const nameSlot = name !== undefined ? { name } : {}
  const extension = cursorExtension(facts.extra)

  if (stored?.payload)
    return stored.payload.name === undefined && name !== undefined ? { ...stored.payload, name } : stored.payload

  if (named === CURSOR_TOOL_UPDATE_TODOS) {
    const items = cursorTodoItems(extension, input)
    return {
      kind: 'todo',
      ...nameSlot,
      label: 'Update TODOs',
      // The frame's own title is the words this row already carries as its LABEL, so
      // it is cleared rather than passed on: `todoRenderer` then composes the count,
      // which is the one thing the header can add. A copy of that wording here would
      // be a second place for it to drift from the renderer's.
      title: undefined,
      // POPULATED, not emptied. `todoRenderer`'s own guard
      // (`role !== 'result' && !hasResultRow`) is what stops a double draw, so an empty
      // list here only hid the to-dos while the call was still running.
      request: { items },
      ...(facts.finished ? { result: { items } } : {}),
    }
  }
  if (named === CURSOR_TOOL_GENERATE_IMAGE) {
    const description = pickString(extension?.params ?? input, 'description')
    const prompt = description || undefined
    return {
      kind: 'image',
      ...nameSlot,
      label: 'Generate Image',
      title: description || pickString(tool, 'title') || undefined,
      // `|| undefined`, never the bare pick. `pickString` answers `''` for an absent
      // key, `ImageRequest.prompt` is optional, and `'' ?? x` is `''` -- so
      // `imageRenderer`'s `call.request.prompt ?? call.title` stopped at the empty
      // string and the header drew `Generate Image` above an empty title.
      request: { ...(prompt !== undefined ? { prompt } : {}) },
      ...(facts.finished ? { result: {} } : {}),
      // The picture is the answer, and it rides the call's own image list rather
      // than a result payload: the shared list draws it beside the prompt.
      images: cursorGeneratedImages(extension),
    }
  }
  if (named === CURSOR_TOOL_ASK_QUESTION) {
    return {
      kind: 'question',
      ...nameSlot,
      label: 'Ask Question',
      title: pickString(input, 'title') || pickString(tool, 'title') || undefined,
      request: { questions: cursorQuestions(input) },
      ...(facts.finished ? { result: { answers: [] } } : {}),
    }
  }
  // A plan arrives in two halves: the stored call carries the tool name alone, and the
  // plan body reaches the transcript as supplemental content the worker recovered from
  // the approval request. A full plan belongs in the transcript, so read both halves.
  const planInput = { ...pickObject(facts.extra, ACP_SUPPLEMENT_REQUEST.RawInput), ...input }
  if (planInput._toolName === CURSOR_TOOL_CREATE_PLAN) {
    const plan = pickString(planInput, 'plan')
    return {
      kind: 'report',
      ...nameSlot,
      label: 'Plan',
      title: pickString(planInput, 'name') || pickString(tool, 'title') || undefined,
      // The plan is what the call PROPOSED: the proposing row draws it from here
      // while the approval is open, and the completing row's result draws it after.
      // A dump of the JSON that carried it is what a reader saw instead, never here.
      // `proposal` is the TYPED field for it, so the shared renderer reads a name
      // the IR declares rather than a key Cursor happens to spell.
      request: { ...(plan ? { proposal: plan } : {}) },
      ...(facts.finished ? { result: proseResult(plan || facts.text, 'markdown') } : {}),
    }
  }
  if (named === CURSOR_TOOL.Task) {
    // The frame states the model that ANSWERED, the runtime's own agent id and the
    // measured duration. The call itself carries the prompt, the description and the
    // requested type, so the two halves together report the run and not the request.
    const details = cursorTaskDetails(extension)
    return cursorAgentCall(facts, { ...input, ...details }, undefined, undefined)
  }
  // The WIRE KIND is the precondition, not the two argument keys alone. This branch
  // sits above the execute, read and search branches, so testing the arguments by
  // themselves rebuilt any call that happened to carry both keys as an MCP card and
  // dropped its command, file or search body.
  if ((facts.wireKind === '' || facts.wireKind === 'other') && pickString(input, 'toolName') && pickString(input, 'providerIdentifier')) {
    const server = pickString(input, 'providerIdentifier')
    const toolName = pickString(input, 'toolName')
    const args = pickObject(input, 'args') ?? {}
    // The shared build owns the card's content blocks; the call states the server
    // and the tool, which the wire kind cannot.
    // `base()` folds '' and 'other' to `mcp`, so its result is an MCP result -- but
    // its TYPE is the whole payload union, and narrowing on the kind is what lets the
    // checker see the two halves belong together.
    const shared = base()
    const result = shared.kind === 'mcp' ? shared.result : undefined
    return {
      ...mcpToolCallRequest(server, toolName, args),
      ...nameSlot,
      ...(result !== undefined ? { result } : {}),
    }
  }
  // The search shape the title and counters state, before any body reads the kind.
  const kind = facts.wireKind === 'search' ? cursorSearchKind(tool, raw) : facts.wireKind
  if (kind === 'execute') {
    const description = pickString(input, 'description')
    const title = description || undefined
    const request = { command: pickString(input, 'command') || '', ...(description ? { description } : {}) }
    if (facts.finished && raw) {
      const stdout = pickString(raw, 'stdout')
      const stderr = pickString(raw, 'stderr')
      const output = [stdout, stderr].filter(Boolean).join(stdout.endsWith('\n') ? '' : '\n') || facts.text
      const exitCode = pickNumber(raw, 'exitCode', undefined)
      return {
        kind,
        ...nameSlot,
        title,
        request,
        result: {
          commands: [{
            output,
            ...(exitCode !== undefined ? { exitCode } : {}),
          }],
          unresolvedTerminals: [],
        },
      }
    }
    if (!facts.finished)
      return { kind, ...nameSlot, title, request }
    // Finished with no protocol record: an interrupted call keeps its partial output
    // in the frame's own content, which the shared command build reads.
    const shared = base()
    return { ...shared, ...nameSlot, title: description || shared.title }
  }
  if (kind === 'read' && tool.status === 'completed' && typeof raw?.content === 'string') {
    const location = Array.isArray(tool.locations) ? tool.locations.find(isObject) : undefined
    const reportedLine = pickNumber(location, 'line', undefined)
    const startLine = reportedLine !== undefined && Number.isSafeInteger(reportedLine) && reportedLine > 0 ? reportedLine : 1
    return {
      kind,
      ...nameSlot,
      request: { path: pickFirstString(input, TOOL_FILE_PATH_KEYS) ?? '' },
      result: readFileResultFromContent({ content: raw.content, startLine }),
    }
  }
  if (isSearchKind(kind) && tool.status === 'completed' && raw) {
    const totalFiles = pickNumber(raw, 'totalFiles', undefined)
    const matches = pickNumber(raw, 'totalMatches', undefined) ?? pickNumber(raw, 'resultCount', undefined)
    if (totalFiles !== undefined || matches !== undefined) {
      const path = pickString(input, 'path')
      return {
        kind,
        ...nameSlot,
        request: { pattern: pickString(input, 'pattern') || '', paths: path ? [path] : [] },
        result: {
          filenames: [],
          content: facts.text,
          numFiles: totalFiles ?? 0,
          numLines: 0,
          ...(matches !== undefined ? { matchCount: matches } : {}),
          truncated: pickBoolean(raw, 'truncated') ?? false,
          fallbackContent: facts.text,
          // POSITIVE EVIDENCE only, for the reason `cursorSearchSource` states: no
          // transcript in `testdata/` records Cursor's own empty wording, so an
          // empty body is the one empty result this build can recognize. Tighten
          // the predicate once a real transcript states that wording.
          empty: facts.text.trim() === '',
        },
      }
    }
  }
  // Cursor's fallback diff parser removes one prefix character from file headers:
  // a new file's diff states `-- /dev/null` above `++ b/<path>`, and the shared
  // parser kept both as content lines.
  if (facts.wireKind === 'edit' || facts.wireKind === 'write') {
    const shared = base()
    // Narrowed on the KIND, so `result.changes` is the file-change result those two
    // kinds declare rather than a shape this branch asserted for itself.
    if ((shared.kind === 'edit' || shared.kind === 'write') && shared.result !== undefined && 'changes' in shared.result) {
      const changes = shared.result.changes.map((source) => {
        const header = `++ b/${source.filePath}\n`
        return source.oldStr === '-- /dev/null' && source.newStr?.startsWith(header)
          ? { ...source, oldStr: '', newStr: source.newStr.slice(header.length) }
          : source
      })
      return { ...shared, result: { changes } }
    }
    return shared
  }
  // The shared build of the kind the title and counters repaired the call to: a
  // search that has not finished, or failed, carries no counter to branch on and
  // still must not fall back to the wire's own `search`. The shared ladder answers
  // the result -- a completed file search whose counters never arrived states the
  // names it printed, where an arguments-only payload drew the header and nothing.
  //
  // The facts are REMAPPED rather than passed on: `input` merges the saved record's
  // arguments over the frame's, and the shared request reads `facts.args`, so a
  // pattern that only the record carried was lost on this path.
  if (kind !== facts.wireKind)
    return acpPayloadFor(acpRemapFacts(facts, { tool: { ...tool, [ACP_SUPPLEMENT_REQUEST.RawInput]: input }, kind }), kind)
  return base()
}

/**
 * Cursor returns file and shell output in rawOutput without ACP content blocks.
 *
 * The two post-conditions every Cursor row carries are applied HERE, around the
 * whole build, rather than at each of its dozen returns: the protocol error a row
 * states nothing else about, and the declined status of a call that never ran.
 */
export const cursorToolCallAdapter: ACPToolCallAdapter = (facts) => {
  const raw = pickObject(facts.tool, ACP_SUPPLEMENT.RawOutput)
  const payload = cursorToolCall(facts)
  // Cursor reports a REFUSED call in `rawOutput`: an approval the reader denied
  // writes `{rejected:true}` -- with a `reason` on a web search and a web fetch,
  // and without one on an MCP call -- and a policy refusal writes
  // `{permissionDenied:true}`. Both mean the tool never ran, which `failed` would
  // misreport as a tool that tried.
  if (pickBoolean(raw, 'rejected') === true || pickBoolean(raw, 'permissionDenied') === true)
    return { ...payload, statusOverride: 'declined', result: failedResult(pickString(raw, 'reason') || facts.text) }
  // Cursor reports a protocol-level failure in `rawOutput.error` and writes no
  // output beside it. A build that restored no payload from the saved result is
  // empty for exactly that reason -- a failed `mcp_*` call whose saved result is
  // empty is the reachable case, and its card carries no content block at all.
  //
  // A call the shared build answered needs nothing here: `collectAcpToolText` reads
  // `rawOutput.error` for a frame that carries no content block, so the ladder has
  // already stated the same words as the call's failure.
  const error = pickString(raw, 'error')
  // `'content' in card` is the narrowing, not an assertion. An MCP result slot also
  // holds a failure and an unparsed answer, and neither of those carries a block list.
  const card = payload.kind === 'mcp' ? payload.result : undefined
  const emptyCard = card !== undefined && 'content' in card && card.content.length === 0
  return error && facts.finished && (payload.result === undefined || emptyCard) ? { ...payload, result: unparsedResult(error) } : payload
}
