import type { JsonValue } from '@bufbuild/protobuf'
import type { ControlPrompt } from './model/controlPrompt'
import type { PersistedControlResponse } from './persistedControlResponse'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { fromJson } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { ControlPromptSchema } from '~/generated/proto/leapmux/v1/agent_control_pb'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { controlPromptFromWire } from './model/controlPromptWire'
import { resolveControlResponseSummary } from './persistedControlResponse'
import { pluginFor } from './providers/registry'
import './providers'

/**
 * The browser's half of testdata/saved_decision_corpus.json.
 *
 * Each case is a saved permission decision captured from an installed runtime: the
 * request and the response that the Worker stores, and the prompt that it records
 * beside them. The Worker's half (`TestSavedDecisionCorpusPrompts`) proves that the
 * provider reads the request into that prompt. This half proves that the row reads the
 * words its own control carried, from those three and from nothing else.
 */
interface SavedDecisionCase {
  name: string
  provider: string
  label: string
  request: Record<string, unknown>
  response: Record<string, unknown>
  prompt: JsonValue
}

const fixturePath = resolve(dirname(fileURLToPath(import.meta.url)), '../../../../testdata/saved_decision_corpus.json')
const fixture = JSON.parse(readFileSync(fixturePath, 'utf8')) as { cases: SavedDecisionCase[] }

const PROVIDER_PREFIX = 'AGENT_PROVIDER_'

function providerOf(token: string): AgentProvider {
  const provider = AgentProvider[token.slice(PROVIDER_PREFIX.length) as keyof typeof AgentProvider]
  if (provider === undefined)
    throw new Error(`the case states the unknown provider ${token}`)
  return provider
}

/** The recorded prompt of one case, in the model that a display reads. */
function promptOf(entry: SavedDecisionCase): ControlPrompt {
  return controlPromptFromWire(fromJson(ControlPromptSchema, entry.prompt))
}

/** A snake_case identifier, which is a wire vocabulary rather than a reader's. */
const WIRE_TOKEN = /^[a-z][a-z0-9]*(?:_[a-z0-9]+)+$/

describe('saved decision corpus', () => {
  it('holds cases', () => {
    expect(fixture.cases.length).toBeGreaterThan(0)
  })

  it.each(fixture.cases)('reads $name back as its own words', (entry) => {
    const display = resolveControlResponseSummary(
      storedResponse(entry.request, entry.response, promptOf(entry)),
      pluginFor(providerOf(entry.provider))?.controls?.controlResponseDisplay,
    )
    expect(display).toEqual({ kind: 'label', text: entry.label })
  })

  // The words for an option the agent named can come from the stored REQUEST, and a
  // row can outlive it: an enrichment that failed validation is dropped while the
  // original message is kept. What the reader must never see then is the wire token
  // the answer carried -- `allow_once` where "Allow once" belongs. Each provider
  // degrades to its own canonical words or to the neutral fallback, and none to a raw
  // token.
  it.each(fixture.cases)('shows no wire token for $name when the request is absent', (entry) => {
    const display = resolveControlResponseSummary(
      storedResponse(undefined, entry.response, promptOf(entry)),
      pluginFor(providerOf(entry.provider))?.controls?.controlResponseDisplay,
    )
    expect(display.kind).toBe('label')
    if (display.kind === 'label') {
      expect(display.text).not.toBe('')
      expect(display.text).not.toMatch(WIRE_TOKEN)
    }
  })
})

/** One stored row, as `parsePersistedControlResponse` would hand it to the renderer. */
function storedResponse(
  request: Record<string, unknown> | undefined,
  response: Record<string, unknown>,
  prompt: ControlPrompt,
): PersistedControlResponse {
  // The claim token never reaches a derivation. It is present because the type requires
  // it, and its value is deliberately not one.
  return {
    requestId: 'request',
    claimToken: 'not-a-token',
    request,
    response,
    prompt,
  }
}
