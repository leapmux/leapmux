import type { ACPToolCallAdapter } from '../../acp/extractors/toolCall'
import { ACP_SUPPLEMENT_REQUEST } from '~/generated/contracts/acp-protocol'
import { DIRAC_TOOL } from '~/generated/contracts/dirac-protocol'
import { isObject, pickString } from '~/lib/jsonPick'
import { acpRemapFacts, acpSpecFor } from '../../acp/extractors/toolCall'

/**
 * The `rawInput` field that names Dirac's tool. Dirac stamps the model-facing
 * tool name on every raw input, which is the one reliable identifier on its
 * wire: `title` is the card's display label ("Run Subagents"), not the tool.
 */
const DIRAC_RAW_INPUT_TOOL_FIELD = 'tool'

/**
 * Dirac's reading of one ACP tool call.
 *
 * Dirac maps every model tool to an ACP `tool_call` whose `kind` is the
 * behavioural category (`execute`, `edit`, …), with the tool name on
 * `rawInput.tool`. The shared builder already reads the kind; this adapter
 * upgrades the aggregate `use_subagents` card to an `agent` row and rewrites an
 * `edit_file` call's `files[].edits[]` into the shared one-file edit shape, so
 * the file change carries its path and both sides of each substitution.
 */
export const diracToolCallAdapter: ACPToolCallAdapter = (facts, base) => {
  const name = pickString(facts.args, DIRAC_RAW_INPUT_TOOL_FIELD)
  if (name === DIRAC_TOOL.UseSubagents) {
    const description = pickString(facts.args, 'task_title') ?? pickString(facts.tool, 'title') ?? DIRAC_TOOL.UseSubagents
    const prompt = pickString(facts.args, 'prompt') ?? ''
    const rawInput = isObject(facts.tool[ACP_SUPPLEMENT_REQUEST.RawInput])
      ? { ...(facts.tool[ACP_SUPPLEMENT_REQUEST.RawInput] as Record<string, unknown>), description, prompt }
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
 * Dirac's `edit_file` states its target as `files: [{path, edits: [{edit_type,
 * anchor, end_anchor?, text}]}]`. The shared builder reads one file with
 * `edits` substitutions under the old/new key spellings, so this flattens the
 * first file into that shape and takes each substitution's old text from the
 * anchor's content half (`Word§content`): the anchor names the line the edit
 * replaces. Returns undefined for a call whose input carries no file.
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
      // `pickString` answers "" for a missing key, so `||` (not `??`) falls
      // through to the anchor's content half.
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
