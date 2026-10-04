import type { ACPToolCallAdapter } from '../../acp/extractors/toolCall'
import { ACP_SUPPLEMENT_REQUEST } from '~/generated/contracts/acp-protocol'
import { DIRAC_TOOL } from '~/generated/contracts/dirac-protocol'
import { isObject, pickString } from '~/lib/jsonPick'
import { acpRemapFacts, acpSpecFor } from '../../acp/extractors/toolCall'
import { DIRAC_RAW_INPUT_TOOL_FIELD } from '../protocol'
import { diracExecuteSpec } from './execute'

/**
 * Read Dirac's tool facts into the shared model.
 * Native rows give the tool name in rawInput.tool or name.
 * Execute reads the native exit fields beside the output.
 * The use_subagents card becomes an agent row.
 * File edits keep their path and both sides of each substitution.
 */
export const diracToolCallAdapter: ACPToolCallAdapter = (facts, base) => {
  const name = pickString(facts.args, DIRAC_RAW_INPUT_TOOL_FIELD) || pickString(facts.tool, 'name')
  if (name === DIRAC_TOOL.ExecuteCommand)
    return diracExecuteSpec(facts, base)
  if (name === DIRAC_TOOL.UseSubagents) {
    const description = pickString(facts.args, 'task_title') || pickString(facts.tool, 'title') || DIRAC_TOOL.UseSubagents
    const prompt = pickString(facts.args, 'prompt')
    const nativeInput = facts.tool[ACP_SUPPLEMENT_REQUEST.RawInput]
    const rawInput = isObject(nativeInput)
      ? { ...nativeInput, description, prompt }
      : { description, prompt }
    const remapped = acpRemapFacts(facts, {
      tool: { ...facts.tool, [ACP_SUPPLEMENT_REQUEST.RawInput]: rawInput },
      kind: 'agent',
    })
    return acpSpecFor(remapped, 'agent')
  }
  const flattened = diracFileChangeArgs(facts.args)
  if (flattened === undefined)
    return base()
  return acpSpecFor(acpRemapFacts(facts, {
    tool: { ...facts.tool, [ACP_SUPPLEMENT_REQUEST.RawInput]: flattened },
    kind: facts.wireKind,
  }), facts.wireKind)
}

/**
 * Dirac's edit_file gives a files array with edits inside each file.
 * The shared builder reads one file with old and new substitution text.
 * Flatten the first file and read old text from the anchor's content half.
 * Word§content identifies the line that the edit replaces.
 * Return undefined when the input contains no file.
 */
function diracFileChangeArgs(args: Record<string, unknown>): Record<string, unknown> | undefined {
  const files = Array.isArray(args.files) ? args.files : undefined
  const file = files?.find((entry: unknown) => isObject(entry) && typeof entry.path === 'string' && entry.path !== '')
  if (file === undefined)
    return undefined
  const edits = Array.isArray(file.edits) ? file.edits : []
  return {
    path: file.path,
    edits: edits.flatMap((entry: unknown) => {
      if (!isObject(entry))
        return []
      // pickString returns an empty string for a missing key.
      // Use the anchor's content half when that field is empty.
      const oldStr = pickString(entry, 'old_string') || diracAnchorContent(pickString(entry, 'anchor'))
      const newStr = pickString(entry, 'text') || pickString(entry, 'new_string') || ''
      return oldStr === undefined || oldStr === '' ? [] : [{ old_string: oldStr, new_string: newStr }]
    }),
  }
}

/** The content half of an ANCHOR§CONTENT coordinate, or undefined. */
function diracAnchorContent(anchor: string | undefined): string | undefined {
  const split = anchor?.indexOf('§') ?? -1
  return split >= 0 ? anchor!.slice(split + 1) : undefined
}
