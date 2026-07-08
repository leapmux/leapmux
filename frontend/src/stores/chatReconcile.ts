import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import { MessageSource } from '~/generated/leapmux/v1/agent_pb'
import { isObject } from '~/lib/jsonPick'
import { parseMessageContent } from '~/lib/messageParser'
import { removePersistedLocalMessage } from './chatLocalMessages'

// ---------------------------------------------------------------------------
// Optimistic-local <-> server-echo reconciliation
//
// When a user message is sent it renders immediately as an optimistic local
// (seq 0n). Once the server persists it and echoes it back (a real seq), the
// local must reconcile to that echo so the bubble isn't duplicated. The window
// store calls into these helpers from every path that can carry an echo --
// full-window replace, live append, and the scroll-down merge -- so the "an echo
// reconciles its local away (a failed-but-delivered one included), nothing else"
// rule lives in one place. Extracted from the store: pure matching logic over
// messages, with a single localStorage side-effect (clearing a reconciled local's
// persisted shadow).
// ---------------------------------------------------------------------------

/**
 * Normalize a raw `attachments` array into the minimal {filename, mime_type}
 * shape, dropping any non-string field. Returns undefined for non-array input.
 */
function mapAttachments(raw: unknown): Array<{ filename?: string, mime_type?: string }> | undefined {
  if (!Array.isArray(raw))
    return undefined
  return (raw as Array<{ filename?: string, mime_type?: string }>).map(att => ({
    filename: typeof att?.filename === 'string' ? att.filename : undefined,
    mime_type: typeof att?.mime_type === 'string' ? att.mime_type : undefined,
  }))
}

/**
 * Best-effort plain text for a user message's `content`. Normally a string, but
 * a block-array provider may echo Anthropic-style content blocks; join the text
 * of every `{ type: 'text', text }` block so such an echo normalizes to the same
 * string the optimistic local (always a plain string, see hydrateLocalMessage)
 * signed with. Returns null when no text can be recovered.
 */
function userContentText(content: unknown): string | null {
  if (typeof content === 'string')
    return content
  if (Array.isArray(content)) {
    const text = content
      .filter((b): b is { text: string } => isObject(b) && b.type === 'text' && typeof b.text === 'string')
      .map(b => b.text)
      .join('')
    return text.length > 0 ? text : null
  }
  return null
}

function extractUserMessagePayload(message: AgentChatMessage): { content: string, attachments?: Array<{ filename?: string, mime_type?: string }> } | null {
  if (message.source !== MessageSource.USER)
    return null
  const parsed = parseMessageContent(message)
  const parent = parsed.parentObject
  if (!parent)
    return null
  // The user payload always lives DIRECTLY on the parsed object as `{ content,
  // attachments? }`: LeapMux persists every user message in that flat shape (see the
  // SendAgentMessage handler), and user rows are written by LeapMux -- a provider
  // receives the input, it never re-echoes the user message in its own nested wire
  // shape. So `content`/`attachments` are read straight from `parent`. (`content` is
  // normally a string; userContentText also tolerates a block-array form.)
  const content = userContentText(parent.content)
  if (content === null)
    return null
  const attachments = mapAttachments(parent.attachments)
  return { content, attachments }
}

/**
 * A stable signature over a user message's content + attachments, used to match
 * an optimistic local against the server echo that reproduces it. Returns null
 * for non-user messages (which never reconcile a local).
 */
export function userMessageSignature(message: AgentChatMessage): string | null {
  const payload = extractUserMessagePayload(message)
  if (!payload)
    return null
  return JSON.stringify({
    content: payload.content,
    attachments: payload.attachments?.map(att => ({
      filename: att.filename ?? '',
      mime_type: att.mime_type ?? '',
    })) ?? [],
  })
}

/**
 * True when `seq` is the optimistic-local sentinel: a freshly-sent bubble is assigned
 * seq 0n and pinned to the tail until its server echo arrives, and server-persisted rows
 * always carry seq > 0n -- so a 0n seq unambiguously marks an unassigned local. The
 * seq-level companion to {@link isOptimisticLocal} for sites that hold only a seq (a marks
 * entry, a rail lookup) rather than the whole message, so the "pinned local" rule has one
 * named home instead of a bare `seq === 0n` scattered across the slices.
 */
export function isOptimisticLocalSeq(seq: bigint): boolean {
  return seq === 0n
}

/**
 * True for an optimistic local message: a freshly-sent bubble assigned seq 0n
 * and pinned to the tail until its server echo arrives. The seq-0n marker is the
 * single load-bearing signal the windowing core, both trims, and the reconcile
 * path all key the "pinned local" rule off.
 */
export function isOptimisticLocal(m: AgentChatMessage): boolean {
  return isOptimisticLocalSeq(m.seq)
}

/**
 * The ids of the SERVER (non-optimistic-local) rows in a window snapshot -- the
 * `alreadyPresentServerIds` argument {@link reconcileEchoedLocals} discounts so a
 * second identical send's local can't reconcile against the first send's
 * already-standing echo. Built identically by every reconcile caller (the
 * full-window replace and the scroll-down merge); named here so the "server rows
 * already present" concept has one home and the two call sites can't drift.
 */
export function priorServerIds(rows: readonly AgentChatMessage[]): Set<string> {
  return new Set(rows.filter(m => !isOptimisticLocal(m)).map(m => m.id))
}

/**
 * True for an optimistic local (seq 0n) that is eligible to reconcile to a
 * server echo: a freshly-sent user bubble awaiting its persisted copy. A FAILED
 * send (deliveryError) is STILL a candidate: an arriving server echo proves the
 * message actually went through, so delivery is the truth -- the failed bubble
 * reconciles away to the delivered echo (its orphaned error annotation reclaimed
 * with it). A genuinely-failed send produces no echo, so nothing matches and its
 * error bubble survives. (Excluding deliveryError here was the cause of two
 * divergent outcomes -- a LIVE failure reconciled away while a HYDRATED one, whose
 * proto deliveryError field is set, showed a confusing failed-AND-delivered pair.)
 */
export function isReconcilableLocal(m: AgentChatMessage): boolean {
  return isOptimisticLocal(m) && m.id.startsWith('local-') && m.source === MessageSource.USER
}

/**
 * Match the reconcilable optimistic locals against the server echoes a freshly
 * fetched/replaced `page` carries: return the ids of locals whose user-message
 * signature the page reproduces, and clear each matched local's persisted
 * shadow. Shared by every path that can carry a local's echo -- full-window
 * replace (applyMessages) and the scroll-down merge (mergeFetchedMessages) --
 * so the "an echo reconciles its local away (a failed-but-delivered one included)"
 * rule lives in one place.
 *
 * `alreadyPresentServerIds` is the set of ids of server (non-local) rows already
 * standing in the window before this page. An echo among them was already paired
 * with its local (reconciled live) or is an unrelated older message, so it must
 * NOT count toward reconciling a still-pending local -- see the per-signature
 * counting note below.
 */
export function reconcileEchoedLocals(
  agentId: string,
  page: AgentChatMessage[],
  locals: AgentChatMessage[],
  alreadyPresentServerIds: Set<string> = new Set(),
): Set<string> {
  const reconciled = new Set<string>()
  if (locals.length === 0)
    return reconciled
  // Count echoes per signature, not a plain Set membership: each server echo
  // reconciles AT MOST ONE local. Two identical-text sends produce two locals
  // with the same signature; if the page carries only one of their echoes (the
  // other send is still in flight, or fell on the far side of the page
  // boundary), matching by set membership would drop BOTH locals while inserting
  // only one server bubble -- the still-pending duplicate send would vanish
  // until its own echo happens to arrive. Consuming one echo per local keeps the
  // counts balanced.
  //
  // Count only echoes the page NEWLY introduces: an echo already standing as a
  // server row in the window (alreadyPresentServerIds) was already paired with
  // the local it reconciled live, so counting it again would let a second
  // identical send's local consume the FIRST send's echo and vanish before its
  // own (still in-flight) echo lands. A full-window replace whose page re-lists
  // an already-reconciled echo is the path that exposes this.
  const echoCounts = new Map<string, number>()
  for (const m of page) {
    if (alreadyPresentServerIds.has(m.id))
      continue
    const sig = userMessageSignature(m)
    if (sig !== null)
      echoCounts.set(sig, (echoCounts.get(sig) ?? 0) + 1)
  }
  // Consume echoes against still-pending locals BEFORE previously-failed ones (proto
  // deliveryError), so a failed local can't steal the single echo a pending same-text
  // send is awaiting (which would strand the pending one until its own echo arrives). A
  // failed local reconciles only when no pending same-text local is left to absorb the
  // echo. Stable sort -> send order is preserved within each group.
  const ordered = [...locals].sort((a, b) => Number(!!a.deliveryError) - Number(!!b.deliveryError))
  for (const local of ordered) {
    const sig = userMessageSignature(local)
    if (sig === null)
      continue
    const remaining = echoCounts.get(sig) ?? 0
    if (remaining > 0) {
      reconciled.add(local.id)
      removePersistedLocalMessage(agentId, local.id)
      echoCounts.set(sig, remaining - 1)
    }
  }
  return reconciled
}
