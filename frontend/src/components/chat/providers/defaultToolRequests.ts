import type { FileChangeOperation, FileEditDiff } from '../model/fileEditDiff'
import type { ToolKind } from '../model/toolKind'
import type { ToolRequestByKind } from '../model/tools'
import { prettifyArgsJson } from '~/lib/jsonFormat'
import { pickFirstString, pickNumber, pickString } from '~/lib/jsonPick'
import { TOOL_DESTINATION_PATH_KEYS, TOOL_FILE_PATH_KEYS, TOOL_NEW_TEXT_KEYS, TOOL_OLD_TEXT_KEYS, TOOL_SOURCE_PATH_KEYS, toolInputPaths } from './toolInputKeys'

// The DECLARED request of every tool kind, read from the call's arguments alone.
//
// This is the shared `default*` helper the plugin rule allows: the shape is
// provider-neutral, so every plugin delegates to it rather than spelling one table of
// its own. A kind whose request needs a fact the arguments do not carry stays with its
// provider, as `ToolRequestOverrides` states.
//
// The table takes the ARGUMENTS and nothing else. That is what makes it neutral: a
// `facts` parameter would carry one provider's collected state, and each entry could
// then read it without a reader seeing which provider it now serves.

/** One kind's declared request, filled from the arguments. */
export type DefaultToolRequest<P extends ToolKind> = (args: Record<string, unknown>) => ToolRequestByKind[P]

/**
 * The change a removal ASKED for, from the file its arguments state.
 *
 * A delete has nothing to diff, so the entry states the operation and the path and
 * no content. Without it the row drew the word "Delete" and stated no file at all,
 * because an agent answers a removal with a status and nothing else.
 */
function removalChange(filePath: string | undefined): FileEditDiff[] {
  return filePath ? [{ filePath, operation: 'delete', oldStr: '', newStr: '', structuredPatch: null }] : []
}

/**
 * The change an edit or a write ASKED for, from the file and the two sides its
 * arguments state.
 *
 * `TOOL_OLD_TEXT_KEYS` and `TOOL_NEW_TEXT_KEYS` carry the three spellings a provider
 * sends for the two sides, exactly as `TOOL_FILE_PATH_KEYS` does for the file. A call
 * that states the file and no replacement text still answers ONE change, for the
 * reason `removalChange` gives: the row composes its header from this list at EVERY
 * state of the call, so an empty list heads a failed edit with the word "Edit" and no
 * file at all.
 *
 * The OPERATION is the kind's own. A write adds the whole body it carries, and an edit
 * replaces part of a file that already exists.
 */
function replacementChange(args: Record<string, unknown>, operation: Extract<FileChangeOperation, 'add' | 'edit'>): FileEditDiff[] {
  const filePath = pickFirstString(args, TOOL_FILE_PATH_KEYS)
  if (!filePath)
    return []
  return [{
    filePath,
    operation,
    oldStr: pickFirstString(args, TOOL_OLD_TEXT_KEYS) ?? '',
    newStr: pickFirstString(args, TOOL_NEW_TEXT_KEYS) ?? '',
    structuredPatch: null,
  }]
}

/**
 * The change a move ASKED for, from the two paths its arguments state.
 *
 * A move states a SOURCE and a DESTINATION, and neither is a `filePath`, so the
 * shared file-path list cannot find them on its own -- `TOOL_SOURCE_PATH_KEYS` and
 * `TOOL_DESTINATION_PATH_KEYS` carry the spellings. One path alone still identifies the
 * file the row is about, so the entry stands with the other side absent.
 */
function moveChange(args: Record<string, unknown>): FileEditDiff[] {
  const previousPath = pickFirstString(args, TOOL_SOURCE_PATH_KEYS)
  const destination = pickFirstString(args, TOOL_DESTINATION_PATH_KEYS) ?? pickFirstString(args, TOOL_FILE_PATH_KEYS)
  const filePath = destination ?? previousPath
  if (!filePath)
    return []
  return [{
    filePath,
    // One path alone still identifies the file, so `previousPath` rides only on a real move.
    ...(previousPath && previousPath !== filePath ? { previousPath } : {}),
    operation: 'move',
    oldStr: '',
    newStr: '',
    structuredPatch: null,
  }]
}

/**
 * The shared table. Total over `ToolKind`, and each entry reads the arguments alone.
 *
 * Totality is the mapped type's, so a new `ToolKind` is a compile error here. That
 * matters more on this table than anywhere: the renderers read these fields WITHOUT a
 * guard, so a kind that fell through to `{ args }` reached `call.request.changes[0]` on
 * a running call and threw the whole message into the ErrorBoundary.
 *
 * A table rather than a `switch`. A switch narrows the value it tests and never the
 * type parameter, so every case had to be asserted back to `K`'s request at the return
 * -- and a case that answered another kind's shape compiled. Here each entry is checked
 * against its own kind.
 *
 * EVERY entry declares its own return type, and the annotation is load-bearing rather
 * than decorative. The mapped type supplies a contextual SIGNATURE, which is not an
 * annotated position: TypeScript infers an un-annotated arrow's return type from the
 * literals it returns, so the object loses its freshness before any property is checked
 * and the excess-property check never runs. `'fetch': args => ({ url, patchText })`
 * compiles with the undeclared key, and the model then carries a field no renderer reads.
 * `'fetch': (args): ToolRequestByKind['fetch'] =>` rejects it.
 * `toolTableEntriesAreAnnotated.test.ts` keeps every entry in that form.
 */
export const DEFAULT_TOOL_REQUESTS: { [P in ToolKind]: DefaultToolRequest<P> } = {
  // A launch states its own description when the arguments carry one. A provider whose
  // frame states the description somewhere else overrides this entry.
  agent: (args): ToolRequestByKind['agent'] => ({ description: pickString(args, 'description') || '', prompt: pickString(args, 'prompt') || pickString(args, 'instructions') || '' }),
  agents: (args): ToolRequestByKind['agents'] => {
    const channel = pickString(args, 'channel')
    const query = pickString(args, 'q') || pickString(args, 'query')
    return { ...(channel ? { channel } : {}), ...(query ? { query } : {}) }
  },
  chart: (args): ToolRequestByKind['chart'] => ({ spec: pickString(args, 'spec') || '' }),
  delete: (args): ToolRequestByKind['delete'] => ({ changes: removalChange(pickFirstString(args, TOOL_FILE_PATH_KEYS)) }),
  move: (args): ToolRequestByKind['move'] => ({ changes: moveChange(args) }),
  glob: (args): ToolRequestByKind['glob'] => ({ pattern: pickString(args, 'pattern') || pickString(args, 'query') || '', paths: toolInputPaths(args) }),
  grep: (args): ToolRequestByKind['grep'] => ({ pattern: pickString(args, 'pattern') || pickString(args, 'query') || '', paths: toolInputPaths(args) }),
  image: (args): ToolRequestByKind['image'] => {
    const prompt = pickString(args, 'prompt')
    return prompt ? { prompt } : {}
  },
  list: (args): ToolRequestByKind['list'] => ({ path: pickFirstString(args, TOOL_FILE_PATH_KEYS) || '.' }),
  memory: (args): ToolRequestByKind['memory'] => (Object.keys(args).length > 0 ? { payload: args } : {}),
  report: (args): ToolRequestByKind['report'] => (Object.keys(args).length > 0 ? { payload: args } : {}),
  message: (args): ToolRequestByKind['message'] => {
    const to = pickString(args, 'to')
    const summary = pickString(args, 'summary')
    return { ...(to ? { to } : {}), text: pickString(args, 'text') || pickString(args, 'message') || '', ...(summary ? { summary } : {}) }
  },
  question: (): ToolRequestByKind['question'] => ({ questions: [] }),
  skill: (args): ToolRequestByKind['skill'] => {
    const name = pickString(args, 'name') || pickString(args, 'skill')
    return { ...(name ? { name } : {}), ...(Object.keys(args).length > 0 ? { args: prettifyArgsJson(args) } : {}) }
  },
  task: (args): ToolRequestByKind['task'] => {
    const taskId = pickString(args, 'task_id') || pickString(args, 'taskId')
    return { action: 'other', ...(taskId ? { taskId } : {}) }
  },
  // The thought the arguments carry. A provider whose thought arrives as the call's
  // own content, and not as an argument, overrides this entry.
  think: (args): ToolRequestByKind['think'] => ({ text: pickString(args, 'thought') || pickString(args, 'text') || '' }),
  todo: (): ToolRequestByKind['todo'] => ({ items: [] }),
  // Four facts and five keys, because a scheduled job states its id, its label and its
  // schedule the same way whichever provider sends it. `action` is the exception and
  // stays `other` here: the providers that state one state it in the TOOL NAME, which
  // this table never sees, so each of those overrides this entry for that field alone.
  trigger: (args): ToolRequestByKind['trigger'] => {
    const triggerId = pickString(args, 'trigger_id') || pickString(args, 'triggerId') || pickString(args, 'id')
    const name = pickString(args, 'name')
    const schedule = pickString(args, 'schedule') || pickString(args, 'cron')
    return {
      action: 'other',
      ...(triggerId ? { triggerId } : {}),
      ...(name ? { name } : {}),
      ...(schedule ? { schedule } : {}),
    }
  },
  wait: (): ToolRequestByKind['wait'] => ({}),
  web_search: (args): ToolRequestByKind['web_search'] => ({ query: pickString(args, 'query') || pickString(args, 'q') || '' }),
  edit: (args): ToolRequestByKind['edit'] => ({ changes: replacementChange(args, 'edit') }),
  write: (args): ToolRequestByKind['write'] => ({ changes: replacementChange(args, 'add') }),
  execute: (args): ToolRequestByKind['execute'] => {
    const description = pickString(args, 'description')
    return { command: pickString(args, 'command') || pickString(args, 'cmd') || '', ...(description ? { description } : {}) }
  },
  fetch: (args): ToolRequestByKind['fetch'] => ({ url: pickString(args, 'url') || pickString(args, 'uri') || '' }),
  mcp: (args): ToolRequestByKind['mcp'] => ({ args, server: pickString(args, 'server'), tool: pickString(args, 'tool') }),
  read: (args): ToolRequestByKind['read'] => {
    const offset = pickNumber(args, 'offset', undefined)
    const limit = pickNumber(args, 'limit', undefined)
    return {
      path: pickFirstString(args, TOOL_FILE_PATH_KEYS) || '',
      ...(offset !== undefined ? { offset } : {}),
      ...(limit !== undefined ? { limit } : {}),
    }
  },
  search: (args): ToolRequestByKind['search'] => ({ pattern: pickString(args, 'pattern') || pickString(args, 'query') || '', paths: toolInputPaths(args) }),
  // Two fields, and three keys, because the kind carries two separate facts. `mode`
  // is where the session lands, and the Agent Client Protocol spells it `targetModeId`
  // as well. `target` is what the switch acts on -- the worktree name that
  // `switchModeRenderer` draws after the mode -- so it never folds into `mode`.
  switch_mode: (args): ToolRequestByKind['switch_mode'] => {
    const mode = pickString(args, 'mode') || pickString(args, 'targetModeId')
    const target = pickString(args, 'target')
    return { ...(mode ? { mode } : {}), ...(target ? { target } : {}) }
  },
  unspecified: (args): ToolRequestByKind['unspecified'] => ({ args }),
  other: (args): ToolRequestByKind['other'] => ({ args }),
}

/**
 * The kinds ONE provider reads differently, and the facts it reads them from.
 *
 * `F` is that provider's own facts type, so an override takes the state the shared
 * table cannot see. Each entry is checked against its own kind's request, exactly as
 * {@link DEFAULT_TOOL_REQUESTS} is.
 *
 * PARTIAL, and never a copy of the shared table. A provider states the kinds it
 * deviates on and nothing else, so:
 *
 *   - the whole deviation list is the object's own keys, which a reader finds in one
 *     place and a test pins;
 *   - a key that is not a `ToolKind` is a compile error rather than a dead entry.
 *
 * A spread of the shared table still type-checks here, and no type can refuse it: an
 * entry that takes `args` alone satisfies a slot that supplies `args` and the facts. A
 * test over these keys is what catches a table that grew past its deviations, so write
 * one -- `ACP_TOOL_REQUEST_OVERRIDES` carries that test.
 *
 * Write one line at each entry saying which provider fact it reads and why the shared
 * entry cannot supply it. An entry belongs in the shared table instead when BOTH halves
 * hold: it reads `args` alone, AND that reading is provider-NEUTRAL. Neither half is
 * sufficient on its own.
 *
 *   - `ZCODE_TOOL_REQUEST_OVERRIDES.trigger` failed both tests on three of its fields.
 *     It read `name`, `schedule` and `cron` out of the arguments, under the spellings
 *     every provider sends, so those three moved to the shared entry above. What stayed
 *     is the `action`, which the TOOL NAME states and no argument carries.
 *   - `PI_TOOL_REQUEST_OVERRIDES.question` reads `args` alone and stays. It parses Pi's
 *     OWN question record, which no other provider sends, so a shared entry that read it
 *     would state Pi's vocabulary for every provider. The shared `question` entry answers
 *     an empty list exactly so each provider keeps its own.
 *
 * EVERY entry declares its own return type, for the reason {@link DEFAULT_TOOL_REQUESTS}
 * gives: a contextual signature is not an annotated position, so an un-annotated entry
 * takes a stray key without a word.
 */
export type ToolRequestOverrides<F> = { [P in ToolKind]?: (args: Record<string, unknown>, facts: F) => ToolRequestByKind[P] }

/**
 * One kind's declared request: the provider's own reading where it states one, and the
 * shared table's everywhere else.
 *
 * GENERIC over the kind, so `kind` and the request it answers stay one correlated pair.
 * An assertion back to `ToolRequestByKind[K]` is what a `switch` needed here, and that is the
 * one cast the assertion ban in `eslint.config.ts` refuses.
 */
export function toolRequestFor<K extends ToolKind, F>(
  kind: K,
  args: Record<string, unknown>,
  facts: F,
  overrides: ToolRequestOverrides<F>,
): ToolRequestByKind[K] {
  const override = overrides[kind]
  return override ? override(args, facts) : DEFAULT_TOOL_REQUESTS[kind](args)
}
