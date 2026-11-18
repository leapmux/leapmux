import type { ChatRowExtraction } from './rowExtraction'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { AgentProvider, AssembledMessageKind } from '~/generated/proto/leapmux/v1/agent_pb'
import { makeMessage, rawContent } from '~/test-support/messageFactory'
import { providerFor } from './providers/registry'
import { quotableTextForRow } from './results/rowText'
import { prepareChatRow } from './rowPreparation'
import './providers'

/**
 * The browser's half of testdata/reply_text_conformance.json.
 *
 * The package of each provider in the Worker replays its own cases through
 * agent.ReplyText, which fills the reply text that `leapmux control agent
 * send --wait` prints. This replays every case through the row extraction
 * that the transcript draws, so the CLI prints the text that the transcript
 * shows.
 */
interface ReplyTextCase {
  name: string
  provider: string
  content: unknown
  supplemental?: unknown
  agent_session_id?: string
  assembled_kind?: 'text' | 'reasoning' | 'plan'
  reply: string | null
}

const fixturePath = resolve(dirname(fileURLToPath(import.meta.url)), '../../../../testdata/reply_text_conformance.json')
const fixture = JSON.parse(readFileSync(fixturePath, 'utf8')) as { cases: ReplyTextCase[] }

const PROVIDER_PREFIX = 'AGENT_PROVIDER_'

function providerOf(token: string): AgentProvider {
  const provider = AgentProvider[token.slice(PROVIDER_PREFIX.length) as keyof typeof AgentProvider]
  if (provider === undefined)
    throw new Error(`the case states the unknown provider ${token}`)
  return provider
}

const ASSEMBLED_KINDS = {
  text: AssembledMessageKind.TEXT,
  reasoning: AssembledMessageKind.REASONING,
  plan: AssembledMessageKind.PLAN,
} as const

/** The reply that the transcript shows: the quotable text of an assistant text row. */
function replyOf(extraction: ChatRowExtraction): string | null {
  if (extraction.kind !== 'row' || extraction.row.kind !== 'assistant-text')
    return null
  return quotableTextForRow(extraction.row)
}

describe('reply text conformance', () => {
  it('loads a nonempty corpus', () => {
    expect(fixture.cases.length).toBeGreaterThan(0)
  })

  it('has a case with a reply and a case without one for every registered provider', () => {
    const registered = Object.values(AgentProvider)
      .filter((value): value is AgentProvider => typeof value === 'number' && value !== AgentProvider.UNSPECIFIED)
      .filter(provider => providerFor(provider) !== undefined)
    expect(registered.length).toBeGreaterThan(0)
    for (const provider of registered) {
      const cases = fixture.cases.filter(tc => providerOf(tc.provider) === provider)
      expect(cases.some(tc => tc.reply !== null), `${AgentProvider[provider]} has no case with a reply`).toBe(true)
      expect(cases.some(tc => tc.reply === null), `${AgentProvider[provider]} has no case without a reply`).toBe(true)
    }
  })

  it.each(fixture.cases)('$name', (tc) => {
    const message = makeMessage({
      agentProvider: providerOf(tc.provider),
      content: rawContent(tc.content),
      ...(tc.supplemental === undefined
        ? {}
        : {
            supplementalContent: rawContent(tc.supplemental),
          }),
      ...(tc.agent_session_id === undefined
        ? {}
        : { agentSessionId: tc.agent_session_id }),
      ...(tc.assembled_kind === undefined
        ? {}
        : { assembledKind: ASSEMBLED_KINDS[tc.assembled_kind] }),
    })
    expect(replyOf(prepareChatRow(message).extraction)).toBe(tc.reply)
  })
})
