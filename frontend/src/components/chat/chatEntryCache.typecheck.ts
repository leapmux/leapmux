import type { ClassifiedEntryCacheDeps } from './chatEntryCache'
import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ToolSpanRole } from '~/lib/messageSpan'

// The cache must accept the canonical role reader, including explicit no-side records.
declare const canonicalRoleReader: (message: AgentChatMessage) => ToolSpanRole
const cacheRoleReader: ClassifiedEntryCacheDeps['role'] = canonicalRoleReader
const noSideRoleReader: ClassifiedEntryCacheDeps['role'] = () => 'none'

void [cacheRoleReader, noSideRoleReader]
