/**
 * Every tool kind a tool row can carry.
 *
 * A CLOSED set, because four separate tables read the kind -- the icon, the
 * label, the title renderer and the input summary -- and a plain `string` let
 * them disagree. Reasonix reports `move` for a file rename, and three of the
 * four tables had no entry for it: the row drew the generic wrench and repeated
 * its input as raw JSON. Each table is an exhaustive switch, so a new kind
 * is a compile error in every table that must learn it.
 *
 * A member the Agent Client Protocol also spells keeps the WIRE word, so
 * {@link toolKind} is a narrowing for those and never a translation. That is why
 * `switch_mode` keeps its underscore: the protocol spells it that way, and a
 * hyphenated token here would make the narrowing miss every call that carries
 * it. The rest are LeapMux's own words for a category no protocol spells, and
 * each provider's table maps its own tool names onto them.
 *
 * `think` is the ACP protocol's OWN kind for a reasoning step. A tool that asks
 * the reader a question is not a thought, so it takes `question` instead.
 *
 * `search` is a query against a corpus the SESSION holds, and that corpus is not
 * only the file tree. A tool registry, a language-server index and a semantic code
 * search each take it, and Copilot maps a tool onto all three. The boundary against
 * `web_search` is the corpus and never the shape of the query: a search of the open
 * web takes `web_search`, whatever it looks for. `glob` and `grep` are the two file
 * searches that carry a kind of their own, so `search` holds the rest.
 *
 * `SearchResult` declares `filenames` and `lines` for the searches that answer with
 * files, and a search over another corpus leaves both empty. Such a search states
 * its matches in `content`, which the body prints unchanged: `searchResultText`
 * relativizes `lines` alone. A name that is not a path draws as a file the search
 * found if it reaches either file field.
 *
 * `other` and the empty string are reserved for a tool that NO vocabulary lists
 * -- one from a release later than this build, or one a third-party server
 * supplies under a name nothing here can know. A tool a provider's own table
 * lists must never take either, and the per-provider `toolVocabulary` tests fail
 * the suite when one does: the generic row draws a wrench, the word "Other" and
 * a dump of the arguments, which identifies nothing the agent ran.
 */
export const TOOL_KINDS = [
  '',
  'agent',
  'agents',
  'chart',
  'delete',
  'edit',
  'execute',
  'fetch',
  'glob',
  'grep',
  'image',
  'list',
  'mcp',
  'memory',
  'message',
  'move',
  'other',
  'question',
  'read',
  'report',
  'search',
  'skill',
  'switch_mode',
  'task',
  'think',
  'todo',
  'trigger',
  'wait',
  'web_search',
  'write',
] as const

export type ToolKind = (typeof TOOL_KINDS)[number]

const KNOWN_TOOL_KINDS: ReadonlySet<string> = new Set(TOOL_KINDS)

/**
 * Narrows a wire value to one tool kind.
 *
 * A kind LeapMux does not know becomes `other`, which is the catch-all the
 * Agent Client Protocol itself gives for a tool that fits no category. The
 * empty string keeps its own meaning, because a provider that states no kind
 * said something different from a provider that called the tool uncategorized.
 */
export function toolKind(value: string | undefined): ToolKind {
  if (value === undefined)
    return ''
  return KNOWN_TOOL_KINDS.has(value) ? value as ToolKind : 'other'
}
