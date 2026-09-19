import type { CommandExit } from '../../../ir/commandResult'
import type { ToolCallPayload, ToolCallPayloadIR } from '../../../ir/toolCall'
import type { ToolKind } from '../../../ir/toolKind'
import type { ACPToolCallAdapter, ACPToolFacts } from '../../acp/extractors/toolCall'
import { ACP_SUPPLEMENT, ACP_SUPPLEMENT_REQUEST } from '~/generated/contracts/acp-protocol'
import { GOOSE_SUBAGENT } from '~/generated/contracts/goose-protocol'
import { prettifyJson } from '~/lib/jsonFormat'
import { pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { rawTodosToItems } from '~/models/todo'
import { splitExitCodeMarker } from '../../../ir/exitCodeMarker'
import { mcpToolCallRequest, parseMcpContentItem } from '../../../ir/mcpToolCall'
import { readFileResultFromContent } from '../../../ir/readFileResult'
import { failedResult, isFailedResult, isUnparsedResult } from '../../../ir/toolCall'
import { flattenAcpContent } from '../../acp/content'
import { acpPayloadFor, acpRemapFacts, acpToolFacts } from '../../acp/extractors/toolCall'
import { gooseAgentRequest, gooseAgentResult } from '../extractors/agent'
import { gooseSubagentToolCall, isGooseSubagentToolRequest } from '../extractors/subagentToolRequest'
import { GOOSE_DEVELOPER_EXTENSION, GOOSE_DEVELOPER_TOOL, GOOSE_TODO_EXTENSION, GOOSE_TODO_TOOL, GOOSE_TOOL_KINDS, isGooseDeveloperTool } from '../toolKinds'

/**
 * One `read_image` payload, with the file and the size the result states.
 *
 * The image itself already reaches the call: Goose sends it as an ACP image content
 * block beside the text summary, and the shared collector reads it. What that
 * collector cannot supply is:
 *
 *   - The PATH. Goose identifies the file in `rawInput.source`, and `source` is not one of
 *     the three file-path keys the shared fallback tries -- nor should it be, because
 *     several other tools use the word for something that is not a path. Without it
 *     the viewer opens the bytes the row carries instead of the file on disk.
 *   - The SIZE. `rawOutput` states `width` and `height`, so the renderer reserves the
 *     image's space without decoding its header first.
 *
 * `originalWidth` / `originalHeight` are deliberately unread: they describe the file
 * before Goose scaled it down, and the row draws the bytes it received.
 */
function gooseImagePayload<K extends ToolKind>(payload: ToolCallPayload<K>, tool: Record<string, unknown>, input: Record<string, unknown>): ToolCallPayload<K> {
  const raw = pickObject(tool, ACP_SUPPLEMENT.RawOutput)
  const filePath = pickString(raw, 'source') || pickString(input, 'source')
  const width = pickNumber(raw, 'width', undefined)
  const height = pickNumber(raw, 'height', undefined)
  const dimensions = width !== undefined && height !== undefined && width > 0 && height > 0 ? { width, height } : undefined
  // `images` is declared on every payload, so it is read straight off. The old
  // hand-written `{ images?: Array<{ filePath?, dimensions? }> }` intersection was a
  // second, weaker copy of `ImageResultSource` that would keep compiling after the
  // real one gained a field.
  const images = payload.images ?? []
  if (!filePath && !dimensions)
    return payload
  return {
    ...payload,
    label: 'Read Image',
    images: images.map(image => ({
      ...image,
      filePath: image.filePath || filePath || undefined,
      dimensions: image.dimensions ?? dimensions,
    })),
  }
}

/** Goose puts stable tool identity in metadata. Its generated title can change. */
export const gooseToolCallAdapter: ACPToolCallAdapter = (facts, base) => gooseCall(facts, base)

function gooseCall(facts: ACPToolFacts, base: () => ToolCallPayloadIR): ToolCallPayloadIR {
  const tool = facts.tool
  if (isGooseSubagentToolRequest(tool)) {
    // A subagent ASKING to run a tool is a request about a call, not a call of its
    // own. Build the call it asked for from a synthetic frame -- a name that maps
    // to a kind draws exactly what was asked, and a name that maps to none stays
    // on the uncategorized card, whose wrench and requested arguments state the
    // same thing.
    const requested = gooseSubagentToolCall(tool)
    const name = pickString(requested, 'name') || 'tool'
    const args = pickObject(requested, 'arguments') ?? {}
    const frame = {
      ...tool,
      status: 'pending',
      kind: 'other',
      title: `Requested tool: ${name}`,
      rawInput: args,
      content: [],
      rawOutput: undefined,
      _meta: { goose: { toolCall: { toolName: name } } },
    }
    const synthetic = acpToolFacts(frame, undefined)
    const built = gooseCall(synthetic, () => acpPayloadFor(synthetic, 'mcp'))
    return { ...built, label: 'Tool request' }
  }
  const metadata = pickObject(pickObject(pickObject(tool, '_meta'), 'goose'), 'toolCall')
  const fullName = pickString(metadata, 'toolName')
  const separator = fullName.indexOf('__')
  const extension = pickString(metadata, 'extensionName') || (separator >= 0 ? fullName.slice(0, separator) : '')
  const name = separator >= 0 ? fullName.slice(separator + 2) : fullName
  const args = facts.args
  // Goose's platform extensions expose UNPREFIXED tool names, so the `_meta` record
  // is the only place the row's own tool name appears.
  const named = name ? { name } : {}

  if (extension === GOOSE_SUBAGENT.Extension && name === GOOSE_SUBAGENT.Tool) {
    const request = gooseAgentRequest(args)
    return {
      kind: 'agent',
      ...named,
      request,
      // `facts.finished`, never `acpToolFinished(tool)`: the frame alone cannot see
      // the turn's own outcome, so a retained subagent row dropped its report.
      ...(facts.finished ? { result: { agents: [gooseAgentResult(args, facts.text, tool.status)] } } : {}),
    }
  }
  // The OUTCOME is not part of this test. A `todo_write` that failed is still a
  // checklist, and a status test here drops the row to the generic server card. The
  // `content` test stays: a call that carries no list cannot draw one, and an empty
  // checklist claims that the agent cleared the list.
  if (extension === GOOSE_TODO_EXTENSION && name === GOOSE_TODO_TOOL && typeof args.content === 'string') {
    const markdown = pickString(args, 'content')
    const lines = markdown.split(/\r?\n/).filter(line => line.trim() !== '')
    const entries = lines.map(line => /^[-*+] \[([ x])\] (.+)$/i.exec(line))
    // The lifecycle the shared ladder applies to every kind it builds, stated here
    // because this branch answers before that ladder runs. A call that FAILED saved
    // nothing, so the reason it gave is the answer. A call the reader STOPPED keeps
    // the list it collected, which the row marks as partial from its own status.
    const reason = facts.status === 'failed' ? failedResult(facts.text) : null
    if (entries.every(entry => entry !== null)) {
      // `every` above refused a line the pattern did not match, and a match carries
      // both groups -- the bracket and the rest -- so the fallbacks are type-level alone.
      const items = rawTodosToItems(entries.map((entry) => {
        const content = entry?.[2] ?? ''
        const status = entry?.[1]?.toLowerCase() === 'x' ? 'completed' : 'pending'
        return { content, status }
      }))
      return {
        kind: 'todo',
        ...named,
        // No title and a POPULATED request: `todoRenderer` composes the same words from
        // the request, and its own guard (`role !== 'result' && !hasResultRow`) is what
        // stops a double draw -- so emptying the list here only hid it while the call ran.
        request: { items },
        ...(facts.finished ? { result: reason ?? { items } } : {}),
      }
    }
    // A list with headings and nesting is prose about the work, and it stays
    // readable exactly as the agent wrote it.
    return {
      kind: 'todo',
      ...named,
      title: 'To-do list',
      request: { items: [] },
      ...(facts.finished ? { result: reason ?? { items: [], note: markdown } } : {}),
    }
  }
  if (extension === GOOSE_DEVELOPER_EXTENSION) {
    // A PREDICATE, not a bare lookup: `name` is the runtime's own word, and one that
    // spells an `Object.prototype` member answers with a function rather than
    // undefined, so the guard below would not fire.
    if (!isGooseDeveloperTool(name))
      return { ...base(), ...named }
    const kind = GOOSE_TOOL_KINDS[name]
    const input = { ...args }
    // Goose spells the edit halves and the read line with its own words.
    if (name === GOOSE_DEVELOPER_TOOL.Edit) {
      input.oldText = input.before
      input.newText = input.after
    }
    if (name === GOOSE_DEVELOPER_TOOL.Read)
      input.offset = input.line
    // The shared build of the remapped frame: the developer tools speak Goose's own
    // argument names, so the frame is rebuilt with the words the shared extractors
    // read before the per-kind payload is derived from it.
    const remapFacts = acpRemapFacts(facts, { tool: { ...tool, [ACP_SUPPLEMENT_REQUEST.RawInput]: input }, kind })
    if (name === GOOSE_DEVELOPER_TOOL.Tree) {
      // The LABEL alone. The table already states this tool's kind, so the shared
      // build supplies the path request, and its ladder answers a finished call with
      // the words the tool wrote -- which is what this row must draw, because the
      // branch art and the line counts are the point and a flat entry list would
      // redraw them into something the tool did not print.
      return { ...acpPayloadFor(remapFacts, GOOSE_TOOL_KINDS[name]), ...named, label: 'List Files' }
    }
    if (name === GOOSE_DEVELOPER_TOOL.Shell) {
      // Built at `execute`, so `result.commands` is a typed `CommandResult[]` rather
      // than the `Record<string, unknown>` the old intersection widened it to -- which
      // is what let this branch spread a terminal's `signal` and write an `exitCode`
      // beside it, the one pair `CommandExit` exists to forbid.
      const shell = acpPayloadFor(remapFacts, GOOSE_TOOL_KINDS[name])
      const title = pickString(input, 'description') || shell.title
      const prior = shell.result !== undefined && !isFailedResult(shell.result) && !isUnparsedResult(shell.result) ? shell.result : undefined
      if (facts.finished) {
        // A failed shell call carries no `rawOutput` at all. Goose states the code in
        // a content block of its own, ahead of the output: `exit code: 1`. That block
        // is the only statement of it, so without reading it the row says "Error"
        // where every other provider says "Error (exit 1)".
        const raw = pickObject(tool, ACP_SUPPLEMENT.RawOutput)
        const stdout = pickString(raw, 'stdout', undefined)
        const stderr = pickString(raw, 'stderr', undefined)
        const output = stdout !== undefined || stderr !== undefined
          ? [stdout, stderr].filter(value => value !== undefined && value !== '').join(stdout?.endsWith('\n') ? '' : '\n')
          : prior?.commands[0]?.output ?? facts.text
        const marked = splitExitCodeMarker(output)
        const rawExit = pickNumber(raw, 'exit_code', undefined)
        // The line is consumed only when it states the code being reported. A
        // disagreement with `rawOutput.exit_code` is worth showing, so it survives one.
        const consume = marked.exitCode !== undefined && (rawExit === undefined || rawExit === marked.exitCode)
        const exitCode = rawExit ?? marked.exitCode
        const shown = consume ? marked.output : output
        // The exit half is REPLACED, not merged. The prior command came from a
        // terminal, which states a `signal` for a process the OS killed, and writing
        // Goose's code beside it built the one pair `CommandExit` forbids -- so the row
        // drew the green check for a process that was killed. A code Goose reported
        // wins; with none, the terminal's signal stands.
        const { exitCode: _priorExit, signal: priorSignal, ...priorFacts } = prior?.commands[0] ?? { output: shown }
        const exit: CommandExit = exitCode !== undefined
          ? { exitCode }
          : priorSignal !== undefined ? { signal: priorSignal } : {}
        return {
          ...shell,
          title,
          ...named,
          result: {
            commands: [{ ...priorFacts, output: shown, ...exit }],
            unresolvedTerminals: prior?.unresolvedTerminals ?? [],
          },
        }
      }
      return { ...shell, title, ...named }
    }
    if (name === GOOSE_DEVELOPER_TOOL.Read && tool.status === 'completed') {
      const startLine = pickNumber(input, 'line', undefined)
      return { ...acpPayloadFor(remapFacts, GOOSE_TOOL_KINDS[name]), ...named, result: readFileResultFromContent({ content: facts.text, ...(startLine !== undefined ? { startLine } : {}) }) }
    }
    if (name === GOOSE_DEVELOPER_TOOL.ReadImage)
      return { ...gooseImagePayload(acpPayloadFor(remapFacts, GOOSE_TOOL_KINDS[name]), tool, input), ...named }
    return { ...acpPayloadFor(remapFacts, kind), ...named }
  }
  if (!extension || !name || extension === GOOSE_TODO_EXTENSION)
    return { ...base(), ...named }
  // The lifecycle the shared ladder applies to every kind it builds, stated here
  // because this branch answers before that ladder runs. A server call that FAILED
  // gave no answer. Its card is then not the body; the reason it gave is. Without
  // this test the row drew that (usually empty) card and `[no output]` where the
  // reason belongs.
  //
  // A call the reader STOPPED is not a failure, and it keeps the blocks that did
  // arrive: they are the part of the answer the reader asked to see. The header still
  // states `Interrupted`, which `toolRowStatusOutcome` composes from the row's own
  // status.
  const unanswered = facts.status === 'failed'
  return {
    ...mcpToolCallRequest(extension, name, args),
    ...named,
    ...(facts.finished
      ? {
          result: unanswered
            ? failedResult(facts.text)
            : {
                content: flattenAcpContent(tool.content).map(parseMcpContentItem),
                ...(tool.rawOutput !== undefined ? { structuredJson: prettifyJson(tool.rawOutput) } : {}),
              },
        }
      : {}),
  }
}
