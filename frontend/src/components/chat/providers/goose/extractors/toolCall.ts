import type { CommandExit } from '../../../model/commandResult'
import type { ToolCallSpec } from '../../../model/toolCall'
import type { ToolKind } from '../../../model/toolKind'
import type { ACPToolCallAdapter, ACPToolFacts } from '../../acp/extractors/toolCall'
import { rawTodosToItems } from '~/components/chat/normalizers/todo'
import { ACP_SUPPLEMENT, ACP_SUPPLEMENT_REQUEST } from '~/generated/contracts/acp-protocol'
import { GOOSE_SUBAGENT, GOOSE_TODO } from '~/generated/contracts/goose-protocol'
import { prettifyJson } from '~/lib/jsonFormat'
import { isObject, pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { withCommandExit } from '../../../model/commandResult'
import { splitExitCodeMarker } from '../../../model/exitCodeMarker'
import { mcpToolCallRequest, parseMcpContentItem } from '../../../model/mcpToolCall'
import { readFileResultFromContent } from '../../../model/readFileResult'
import { failedResult, isToolFailureResult, isUnparsedToolResult } from '../../../model/toolCall'
import { flattenAcpContent } from '../../acp/content'
import { acpRemapFacts, acpSpecFor, acpToolFacts } from '../../acp/extractors/toolCall'
import { gooseAgentRequest, gooseAgentResult } from '../extractors/agent'
import { gooseSubagentToolCall, isGooseSubagentToolRequest } from '../extractors/subagentToolRequest'
import { GOOSE_CODE_EXECUTION } from '../protocol'
import { GOOSE_DEVELOPER_EXTENSION, GOOSE_DEVELOPER_TOOL, GOOSE_TOOL_KINDS, isGooseDeveloperTool } from '../toolKinds'

/** Read the complete native report. The first line describes script execution, not MCP transport. */
function gooseScriptReport(text: string): { failed: boolean, output: string } | null {
  const match = /^Code Executed Successfully: (true|false)\n\n# Return Value\n```json\n([\s\S]*?)\n```\n\n# STDOUT\n[\s\S]*?\n\n# STDERR\n[\s\S]*$/.exec(text)
  if (!match)
    return null
  const value = match[2]
  if (value === undefined)
    return null
  try {
    JSON.parse(value)
  }
  catch {
    return null
  }
  // Script text can contain the report's section headings. Preserve the complete body instead of splitting ambiguous streams.
  const bodyStart = text.indexOf('\n\n') + 2
  return { failed: match[1] === 'false', output: text.slice(bodyStart) }
}

/**
 * Add the native image file and dimensions to a read_image specification.
 *
 * The shared ACP collector reads the image content block. Goose supplies two extra facts:
 * - rawInput.source identifies the file. Other tools use source for values that are not paths,
 *   so the shared path reader must not treat it as a file field.
 *   This provider reader lets the image viewer open the file on disk.
 * - rawOutput.width and height describe the received image. The renderer can reserve its space without decoding the header first.
 *
 * originalWidth and originalHeight describe the file before scaling.
 * Do not use them for the received image.
 */
function gooseImageSpec<K extends ToolKind>(spec: ToolCallSpec<K>, tool: Record<string, unknown>, input: Record<string, unknown>): ToolCallSpec<K> {
  const raw = pickObject(tool, ACP_SUPPLEMENT.RawOutput)
  const filePath = pickString(raw, 'source') || pickString(input, 'source')
  const width = pickNumber(raw, 'width', undefined)
  const height = pickNumber(raw, 'height', undefined)
  const dimensions = width !== undefined && height !== undefined && width > 0 && height > 0 ? { width, height } : undefined
  // Every specification declares images. Read that type directly instead of creating a weaker duplicate image interface.
  const images = spec.images ?? []
  if (!filePath && !dimensions)
    return spec
  return {
    ...spec,
    label: 'Read Image',
    images: images.map(image => ({
      ...image,
      filePath: image.filePath || filePath || undefined,
      dimensions: image.dimensions ?? dimensions,
    })),
  }
}

/**
 * Goose hooks the `_meta.goose.toolCall` record. Its generated title can change, and its
 * platform extensions send no prefix, so that record is the only stable statement of
 * the tool name.
 */
export const gooseToolCallAdapter: ACPToolCallAdapter = (facts, base) => gooseCall(facts, base)

function gooseCall(facts: ACPToolFacts, base: () => ToolCallSpec): ToolCallSpec {
  const tool = facts.tool
  if (isGooseSubagentToolRequest(tool)) {
    // A subagent tool request describes the call that needs permission.
    // Build that requested call from a synthetic frame.
    // Known tool names retain their kind. Unknown names retain the requested arguments on a generic card.
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
    const built = gooseCall(synthetic, () => acpSpecFor(synthetic, 'mcp'))
    return { ...built, label: 'Tool request' }
  }
  const metadata = pickObject(pickObject(pickObject(tool, '_meta'), 'goose'), 'toolCall')
  const fullName = pickString(metadata, 'toolName')
  const separator = fullName.indexOf('__')
  const extension = pickString(metadata, 'extensionName') || (separator >= 0 ? fullName.slice(0, separator) : '')
  const name = separator >= 0 ? fullName.slice(separator + 2) : fullName
  const args = facts.args
  // Metadata supplies stable tool identity. Platform tool names can omit an extension prefix, and generated titles can change.
  const toolIdentity = name ? { name } : {}

  if (extension === GOOSE_CODE_EXECUTION.Extension && name === GOOSE_CODE_EXECUTION.Tool) {
    const report = facts.finished ? gooseScriptReport(facts.text) : null
    if (report) {
      // Retained cancellation remains authoritative even when the script report describes failure.
      const failed = report.failed && facts.retained !== 'interrupted' && facts.status !== 'cancelled' && facts.status !== 'declined'
      return {
        kind: 'execute',
        ...toolIdentity,
        label: 'Code execution',
        request: { command: pickString(args, 'code'), language: 'javascript' },
        result: { commands: [{ output: report.output, ...(report.failed ? { failed: true as const } : {}) }], unresolvedTerminals: [] },
        ...(failed ? { statusOverride: 'failed' as const } : {}),
      }
    }
    if (!facts.finished && isObject(tool.rawInput) && typeof tool.rawInput.code === 'string') {
      return { kind: 'execute', ...toolIdentity, label: 'Code execution', request: { command: tool.rawInput.code, language: 'javascript' } }
    }
  }

  if (extension === GOOSE_SUBAGENT.Extension && name === GOOSE_SUBAGENT.Tool) {
    const request = gooseAgentRequest(args)
    return {
      kind: 'agent',
      ...toolIdentity,
      request,
      // facts.finished includes the retained turn outcome. The raw frame alone cannot determine whether to preserve a partial report.
      ...(facts.finished ? { result: { agents: [gooseAgentResult(args, facts.text, tool.status)] } } : {}),
    }
  }
  // A failed todo_write remains a checklist. Select its kind independently of the outcome.
  // Require list content before drawing a checklist. An invented empty list would incorrectly report that the agent cleared it.
  if (extension === GOOSE_TODO.Extension && name === GOOSE_TODO.Tool && typeof args.content === 'string') {
    const markdown = pickString(args, 'content')
    const lines = markdown.split(/\r?\n/).filter(line => line.trim() !== '')
    const entries = lines.map(line => /^[-*+] \[([ x])\] (.+)$/i.exec(line))
    // This provider branch runs before the shared result reader.
    // A failed call keeps its failure reason. A stopped call keeps its collected list as partial content.
    const reason = facts.status === 'failed' ? failedResult(facts.text) : null
    if (entries.every(entry => entry !== null)) {
      // every rejects an unmatched line. Each remaining match supplies the bracket state and item text.
      const items = rawTodosToItems(entries.map((entry) => {
        const content = entry?.[2] ?? ''
        const status = entry?.[1]?.toLowerCase() === 'x' ? 'completed' : 'pending'
        return { content, status }
      }))
      return {
        kind: 'todo',
        ...toolIdentity,
        // Keep the request populated. todoRenderer derives its title from that list.
        // Its role and result-row guard prevents duplicate content. An empty request would hide the running list.
        request: { items },
        ...(facts.finished ? { result: reason ?? { items } } : {}),
      }
    }
    // A list with headings and nesting is prose about the work, and it stays
    // readable exactly as the agent wrote it.
    return {
      kind: 'todo',
      ...toolIdentity,
      title: 'To-do list',
      request: { items: [] },
      ...(facts.finished ? { result: reason ?? { items: [], note: markdown } } : {}),
    }
  }
  if (extension === GOOSE_DEVELOPER_EXTENSION) {
    // Validate the native name before the table lookup.
    // A bare lookup could read an Object.prototype function for an absent tool name.
    if (!isGooseDeveloperTool(name))
      return { ...base(), ...toolIdentity }
    const kind = GOOSE_TOOL_KINDS[name]
    const input = { ...args }
    // Goose spells the edit halves and the read line with its own words.
    if (name === GOOSE_DEVELOPER_TOOL.Edit) {
      input.oldText = input.before
      input.newText = input.after
    }
    if (name === GOOSE_DEVELOPER_TOOL.Read)
      input.offset = input.line
    // Remap Goose's native argument keys before the shared kind reader runs.
    // Rebuild the facts also so every shared projection uses the remapped frame.
    const remapFacts = acpRemapFacts(facts, { tool: { ...tool, [ACP_SUPPLEMENT_REQUEST.RawInput]: input }, kind })
    if (name === GOOSE_DEVELOPER_TOOL.Tree) {
      // Replace the label only. The shared reader supplies the path request and native tree output.
      // Preserve the branch characters and line counts instead of converting that output to a flat list.
      return { ...acpSpecFor(remapFacts, GOOSE_TOOL_KINDS[name]), ...toolIdentity, label: 'List Files' }
    }
    if (name === GOOSE_DEVELOPER_TOOL.Shell) {
      // Build the execute kind so result.commands retains CommandResult[] typing.
      // CommandExit forbids a process signal and exit code on the same result.
      const shell = acpSpecFor(remapFacts, GOOSE_TOOL_KINDS[name])
      const title = pickString(input, 'description') || shell.title
      const prior = shell.result !== undefined && !isToolFailureResult(shell.result) && !isUnparsedToolResult(shell.result) ? shell.result : undefined
      if (facts.finished) {
        // A failed shell call can omit rawOutput. Its content can report a separate exit-code marker.
        // Read that marker so the error label preserves the native code.
        const raw = pickObject(tool, ACP_SUPPLEMENT.RawOutput)
        const stdout = pickString(raw, 'stdout', undefined)
        const stderr = pickString(raw, 'stderr', undefined)
        const output = stdout !== undefined || stderr !== undefined
          ? [stdout, stderr].filter(value => value !== undefined && value !== '').join(stdout?.endsWith('\n') ? '' : '\n')
          : prior?.commands[0]?.output ?? facts.text
        const marked = splitExitCodeMarker(output)
        const rawExit = pickNumber(raw, 'exit_code', undefined)
        // Remove the marker only when it agrees with the reported code.
        // Preserve a conflicting marker in the displayed output.
        const consume = marked.exitCode !== undefined && (rawExit === undefined || rawExit === marked.exitCode)
        const exitCode = rawExit ?? marked.exitCode
        const shown = consume ? marked.output : output
        // Replace the exit fields through withCommandExit instead of merging them.
        // A native code takes precedence. Without a code, preserve the existing process signal.
        // Keeping both would violate CommandExit and could show success for a killed process.
        const priorCommand = prior?.commands[0] ?? { output: shown }
        const exit: CommandExit = exitCode !== undefined
          ? { exitCode }
          : priorCommand.signal !== undefined ? { signal: priorCommand.signal } : {}
        return {
          ...shell,
          title,
          ...toolIdentity,
          result: {
            commands: [withCommandExit({ ...priorCommand, output: shown }, exit)],
            unresolvedTerminals: prior?.unresolvedTerminals ?? [],
          },
        }
      }
      return { ...shell, title, ...toolIdentity }
    }
    if (name === GOOSE_DEVELOPER_TOOL.Read && tool.status === 'completed') {
      const startLine = pickNumber(input, 'line', undefined)
      return { ...acpSpecFor(remapFacts, GOOSE_TOOL_KINDS[name]), ...toolIdentity, result: readFileResultFromContent({ content: facts.text, ...(startLine !== undefined ? { startLine } : {}) }) }
    }
    if (name === GOOSE_DEVELOPER_TOOL.ReadImage)
      return { ...gooseImageSpec(acpSpecFor(remapFacts, GOOSE_TOOL_KINDS[name]), tool, input), ...toolIdentity }
    return { ...acpSpecFor(remapFacts, kind), ...toolIdentity }
  }
  if (!extension || !name || extension === GOOSE_TODO.Extension)
    return { ...base(), ...toolIdentity }
  // This provider branch runs before the shared result reader.
  // A failed server call keeps its failure reason instead of an empty result card.
  // A stopped call keeps the content blocks that arrived. Its status still identifies the interruption.
  const unanswered = facts.status === 'failed'
  return {
    ...mcpToolCallRequest(extension, name, args),
    ...toolIdentity,
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
