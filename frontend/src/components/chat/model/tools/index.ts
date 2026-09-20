import type { ProseResult, ProseResultKind } from '../toolCall'
import type { ToolKind } from '../toolKind'
import type { AgentRequest, AgentResult } from './agent'
import type { AgentsRequest, AgentsResult } from './agents'
import type { ChartRequest, ChartResult } from './chart'
import type { DeleteRequest, DeleteResult } from './delete'
import type { EditRequest, EditResult } from './edit'
import type { ExecuteRequest, ExecuteResult } from './execute'
import type { FetchRequest, FetchResult } from './fetch'
import type { GlobRequest, GlobResult } from './glob'
import type { GrepRequest, GrepResult } from './grep'
import type { ImageRequest, ImageResult } from './image'
import type { ListRequest, ListResult } from './list'
import type { McpRequest, McpResult } from './mcp'
import type { MemoryRequest, MemoryResult } from './memory'
import type { MessageRequest, MessageResult } from './message'
import type { MoveRequest, MoveResult } from './move'
import type { OtherRequest, OtherResult } from './other'
import type { QuestionRequest, QuestionResult } from './question'
import type { ReadRequest, ReadResult } from './read'
import type { ReportRequest, ReportResult } from './report'
import type { SearchRequest, SearchResult } from './search'
import type { SkillRequest, SkillResult } from './skill'
import type { SwitchModeRequest, SwitchModeResult } from './switchMode'
import type { TaskRequest, TaskResult } from './task'
import type { ThinkRequest, ThinkResult } from './think'
import type { TodoRequest, TodoResult } from './todo'
import type { TriggerRequest, TriggerResult } from './trigger'
import type { UnspecifiedRequest, UnspecifiedResult } from './unspecified'
import type { WaitRequest, WaitResult } from './wait'
import type { WebSearchRequest, WebSearchResult } from './webSearch'
import type { WriteRequest, WriteResult } from './write'

/** The REQUEST half of every kind's model pair. */
export interface ToolRequestByKind {
  unspecified: UnspecifiedRequest
  agent: AgentRequest
  agents: AgentsRequest
  chart: ChartRequest
  delete: DeleteRequest
  edit: EditRequest
  execute: ExecuteRequest
  fetch: FetchRequest
  glob: GlobRequest
  grep: GrepRequest
  image: ImageRequest
  list: ListRequest
  mcp: McpRequest
  memory: MemoryRequest
  message: MessageRequest
  move: MoveRequest
  other: OtherRequest
  question: QuestionRequest
  read: ReadRequest
  report: ReportRequest
  search: SearchRequest
  skill: SkillRequest
  switch_mode: SwitchModeRequest
  task: TaskRequest
  think: ThinkRequest
  todo: TodoRequest
  trigger: TriggerRequest
  wait: WaitRequest
  web_search: WebSearchRequest
  write: WriteRequest
}

/** The RESULT half of every kind's model pair. */
export interface ToolResultByKind {
  unspecified: UnspecifiedResult
  agent: AgentResult
  agents: AgentsResult
  chart: ChartResult
  delete: DeleteResult
  edit: EditResult
  execute: ExecuteResult
  fetch: FetchResult
  glob: GlobResult
  grep: GrepResult
  image: ImageResult
  list: ListResult
  mcp: McpResult
  memory: MemoryResult
  message: MessageResult
  move: MoveResult
  other: OtherResult
  question: QuestionResult
  read: ReadResult
  report: ReportResult
  search: SearchResult
  skill: SkillResult
  switch_mode: SwitchModeResult
  task: TaskResult
  think: ThinkResult
  todo: TodoResult
  trigger: TriggerResult
  wait: WaitResult
  web_search: WebSearchResult
  write: WriteResult
}

// Compile-time coverage. `ToolCall` indexes both interfaces for every ToolKind, so a
// MISSING key already fails tsc. These reject an EXTRA key, which the index would not see.
type NoExtraToolKindKey<T> = Exclude<keyof T, ToolKind> extends never ? true : never
export const REQUESTS_COVER_TOOL_KINDS: NoExtraToolKindKey<ToolRequestByKind> = true
export const RESULTS_COVER_TOOL_KINDS: NoExtraToolKindKey<ToolResultByKind> = true
// The two brands are reserved: a payload that declared `unparsed` or `failure` would make the predicates lie.
//
// The brand KEYS aggregate, not a per-kind boolean. Indexing a mapped type by the
// whole union takes the UNION of its members, and `true | never` reduces to `true` --
// so a per-kind `true`/`never` guard stayed `true` unless all 30 kinds broke it, and
// the runtime assertion beside it read the literal `true` either way. Collecting the
// offending keys instead makes ONE kind enough to fail the annotation.
type DistributedKeys<T> = T extends unknown ? keyof T : never
type ToolResultBrandKey<T> = Extract<DistributedKeys<T>, 'unparsed' | 'failure'>
export const NO_PAYLOAD_RESERVES_A_BRAND: { [K in ToolKind]: ToolResultBrandKey<ToolResultByKind[K]> }[ToolKind] extends never ? true : never = true

// `DeclinedToolCallState` narrows its typed half with `Extract<ToolResultByKind[K], ProseResult>`,
// and `toolCallFault` reads the same set at RUNTIME from `PROSE_RESULT_KINDS` --
// which no type checks unless something states the two are one set. These do, in both
// directions: a kind whose result becomes prose, and a kind whose result stops being
// prose, each fail one of the two annotations.
type ToolKindWithProseResult = { [K in ToolKind]: ToolResultByKind[K] extends ProseResult ? K : never }[ToolKind]
export const PROSE_RESULT_KINDS_MATCH_THE_TYPES: ProseResultKind extends ToolKindWithProseResult ? true : never = true
export const EVERY_PROSE_RESULT_KIND_IS_LISTED: ToolKindWithProseResult extends ProseResultKind ? true : never = true
