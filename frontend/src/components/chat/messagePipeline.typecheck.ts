// @ts-expect-error The orchestration context is private to MessageBubble.
import type { RenderContext } from './messageRenderers'
import type { ClassificationInput } from './providers/registry'
import type { ResolvedMessageContent, RowExtractionInput } from './rowExtractionTypes'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { acpSpanRole, createACPRelatedMessagesReader } from './providers/acp/spanRole'
import { claudeRelatedMessages, claudeSpanRole } from './providers/claude/spanRole'
import { codexRelatedMessages, codexSpanRole } from './providers/codex/spanRole'
import { copilotRelatedMessages, copilotSpanRole } from './providers/copilot/spanRole'
import { piRelatedMessages, piSpanRole } from './providers/pi/spanRole'
import { resolveMessageForRendering } from './providers/registry'
import { zcodeRelatedMessages, zcodeSpanRole } from './providers/zcode/spanRole'

// The RESOLVED-PAYLOAD boundary, compile-only.
//
// `ResolvedMessageContent` is branded, and `resolveMessageForRendering()` is its
// one constructor. These expectations pin the boundary from both sides: a RAW
// parse cannot reach the classifiers, the span-role readers or the extractors,
// and a value the constructor returned can reach all three. A refactor that
// loosens either half fails `tsc` here rather than letting raw bytes reach a
// renderer. This module exports nothing and no runner executes it.

declare const brandProbe: unique symbol
declare const privateRenderContext: RenderContext
void privateRenderContext

/** A raw parse, as the parser hands it to the resolver. */
const RAW: ParsedMessageContent = { wrapper: null, topLevel: null, parentObject: undefined, rawText: '', supplementalContent: undefined, messageMetadata: undefined }

/** The envelope fields a classification input adds over the parse. */
const ENVELOPE = { agentProvider: AgentProvider.CLAUDE_CODE }

// A raw parse cannot reach `classify`.
// @ts-expect-error The classifier reads the MERGED payload; a raw parse would classify bytes no row displays.
const rawToClassify: ClassificationInput = { ...RAW, ...ENVELOPE }
void rawToClassify

// A raw parse cannot reach a provider's `spanRole` hook. The shared helper takes
// a raw parse BY DESIGN (it resolves first); the hook itself does not.
declare const spanRoleHook: (parsed: ResolvedMessageContent) => string
// @ts-expect-error The role reader sees the merged payload; a raw parse would pair the span on stale bytes.
const rawToSpanRole: string = spanRoleHook(RAW)
void rawToSpanRole

// Raw parses cannot reach an exported transcript hook through a direct import.
// @ts-expect-error ACP span roles read the merged payload.
void acpSpanRole(RAW)
// @ts-expect-error ACP related-message selection reads the merged payload.
void createACPRelatedMessagesReader()(RAW)
// @ts-expect-error Claude span roles read the merged payload.
void claudeSpanRole(RAW)
// @ts-expect-error Claude related-message selection reads the merged payload.
void claudeRelatedMessages(RAW)
// @ts-expect-error Codex span roles read the merged payload.
void codexSpanRole(RAW)
// @ts-expect-error Codex related-message selection reads the merged payload.
void codexRelatedMessages(RAW)
// @ts-expect-error Copilot span roles read the merged payload.
void copilotSpanRole(RAW)
// @ts-expect-error Copilot related-message selection reads the merged payload.
void copilotRelatedMessages(RAW)
// @ts-expect-error Pi span roles read the merged payload.
void piSpanRole(RAW)
// @ts-expect-error Pi related-message selection reads the merged payload.
void piRelatedMessages(RAW)
// @ts-expect-error ZCode span roles read the merged payload.
void zcodeSpanRole(RAW)
// @ts-expect-error ZCode related-message selection reads the merged payload.
void zcodeRelatedMessages(RAW)

// A raw parse cannot reach `extractRow` -- not its payload slot, and not a side.
// @ts-expect-error Extraction reads the merged payload; a raw parse would extract a row its own category never saw.
const rawParsedSlot: RowExtractionInput['resolved'] = RAW
// @ts-expect-error A side the caller resolved carries the same brand a payload does.
const rawSideSlot: RowExtractionInput['span']['request'] = RAW
void [rawParsedSlot, rawSideSlot]

// A value the constructor returned reaches all three.
const RESOLVED: ResolvedMessageContent = resolveMessageForRendering(RAW, AgentProvider.CLAUDE_CODE)
const resolvedToClassify: ClassificationInput = { ...RESOLVED, ...ENVELOPE }
const resolvedToSpanRole: string = spanRoleHook(RESOLVED)
const resolvedToExtractRow: RowExtractionInput = {
  resolved: RESOLVED,
  category: { kind: 'unknown' },
  span: { request: undefined, result: undefined, role: 'other', visibleRows: { request: false, result: false } },
}
void [resolvedToClassify, resolvedToSpanRole, resolvedToExtractRow]

// The brand itself is not spellable from outside: a structurally identical
// object with an extra harmless member is still a raw parse.
declare const extraBrandProbe: unique symbol
// @ts-expect-error Only the one constructor brands the parse.
const fakeBrand: ResolvedMessageContent = { ...RAW, [extraBrandProbe]: true }
void [fakeBrand, brandProbe]
