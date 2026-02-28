/** Typed interfaces for known tool inputs from Claude Code agent messages. */

export interface BashInput {
  command?: string
  description?: string
  timeout?: number
  run_in_background?: boolean
}

export interface ReadInput {
  file_path?: string
  offset?: number
  limit?: number
  pages?: string
}

export interface WriteInput {
  file_path?: string
  content?: string
}

export interface EditInput {
  file_path?: string
  old_string?: string
  new_string?: string
  replace_all?: boolean
}

export interface GrepInput {
  pattern?: string
  path?: string
  glob?: string
  type?: string
  output_mode?: string
  head_limit?: number
}

export interface GlobInput {
  pattern?: string
  path?: string
}

export interface TaskInput {
  description?: string
  prompt?: string
  subagent_type?: string
}

export interface WebFetchInput {
  url?: string
  prompt?: string
}

export interface WebSearchInput {
  query?: string
}

export interface TodoWriteInput {
  todos?: Array<{
    content?: string
    status?: string
    activeForm?: string
  }>
}

export interface TaskOutputInput {
  task_id?: string
  block?: boolean
  timeout?: number
}

export interface AskUserQuestionInput {
  questions?: Array<{
    question?: string
    header?: string
    options?: Array<{
      label?: string
      description?: string
    }>
    multiSelect?: boolean
  }>
}

/** Discriminated union of all known tool input types keyed by tool name. */
export type KnownToolInput
  = | { toolName: 'Bash', input: BashInput }
    | { toolName: 'Read', input: ReadInput }
    | { toolName: 'Write', input: WriteInput }
    | { toolName: 'Edit', input: EditInput }
    | { toolName: 'Grep', input: GrepInput }
    | { toolName: 'Glob', input: GlobInput }
    | { toolName: 'Task', input: TaskInput }
    | { toolName: 'WebFetch', input: WebFetchInput }
    | { toolName: 'WebSearch', input: WebSearchInput }
    | { toolName: 'TodoWrite', input: TodoWriteInput }
    | { toolName: 'TaskOutput', input: TaskOutputInput }
    | { toolName: 'AskUserQuestion', input: AskUserQuestionInput }

/** All known tool names. */
export type KnownToolName = KnownToolInput['toolName']

const KNOWN_TOOLS = new Set<string>([
  'Bash',
  'Read',
  'Write',
  'Edit',
  'Grep',
  'Glob',
  'Task',
  'WebFetch',
  'WebSearch',
  'TaskOutput',
  'TodoWrite',
  'AskUserQuestion',
])

/** Type guard: returns true if the tool name is a known tool. */
export function isKnownTool(name: string): name is KnownToolName {
  return KNOWN_TOOLS.has(name)
}
