import type { JsonValue } from '@bufbuild/protobuf'
import type { ControlPrompt } from './model/controlPrompt'
import type { PersistedControlResponse } from './persistedControlResponse'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { create, fromJson } from '@bufbuild/protobuf'
import { afterEach, describe, expect, it } from 'vitest'
import { ControlDecisionSchema, ControlPromptSchema } from '~/generated/proto/leapmux/v1/agent_control_pb'
import { AgentProvider, AvailableOptionGroupSchema } from '~/generated/proto/leapmux/v1/agent_pb'
import { clearSettingsLabelCache, updateSettingsLabelCache } from '~/lib/settingsLabelCache'
import { controlPromptFromWire } from './model/controlPromptWire'
import { resolveControlResponseSummary } from './persistedControlResponse'

/**
 * The browser's half of testdata/saved_decision_corpus.json.
 *
 * Each case is a saved permission decision captured from an installed runtime: the
 * request and the response that the Worker stores, and the prompt and the decision
 * that it records beside them. The Worker's half proves that the provider reads the
 * request into that prompt (`TestSavedDecisionCorpusPrompts`), and that the decision
 * rebuilds the response (`TestSavedDecisionCorpusDecisions`). This half proves that
 * the row reads its words from the prompt, the decision, and the labels of the
 * agent's permission modes alone.
 */
interface SavedDecisionCase {
  name: string
  provider: string
  requestId: string
  label: string
  request: Record<string, unknown>
  response: Record<string, unknown>
  prompt: JsonValue
  decision: JsonValue
  /** The labels of the agent's permission modes that the browser holds, by mode value. */
  permissionModeLabels?: Record<string, string>
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

  afterEach(() => clearSettingsLabelCache())

  it.each(fixture.cases)('reads $name back as its own words', (entry) => {
    holdPermissionModeLabels(entry)
    expect(resolveControlResponseSummary(storedResponse(entry), providerOf(entry.provider))).toEqual({ kind: 'label', text: entry.label })
  })

  // The words come from the prompt and the decision, so neither half that the agent
  // wrote can change them: a row whose request and response are gone reads the same.
  it.each(fixture.cases)('reads $name without its native request and response', (entry) => {
    holdPermissionModeLabels(entry)
    const display = resolveControlResponseSummary({
      ...storedResponse(entry),
      request: undefined,
      response: undefined,
    }, providerOf(entry.provider))
    expect(display).toEqual({ kind: 'label', text: entry.label })
    if (display.kind === 'label')
      expect(display.text).not.toMatch(WIRE_TOKEN)
  })
})

/** Hold the labels of the case's permission modes, as the browser holds an open agent's settings. */
function holdPermissionModeLabels(entry: SavedDecisionCase): void {
  if (!entry.permissionModeLabels)
    return
  updateSettingsLabelCache(providerOf(entry.provider), [
    create(AvailableOptionGroupSchema, {
      id: 'permissionMode',
      options: Object.entries(entry.permissionModeLabels).map(([id, name]) => ({ id, name })),
    }),
  ])
}

/** One stored row, as `parsePersistedControlResponse` would hand it to the renderer. */
function storedResponse(entry: SavedDecisionCase): PersistedControlResponse {
  // The claim token never reaches a display. It is present because the type requires
  // it, and its value is deliberately not one.
  return {
    requestId: entry.requestId,
    claimToken: 'not-a-token',
    request: entry.request,
    response: entry.response,
    prompt: promptOf(entry),
    decision: fromJson(ControlDecisionSchema, entry.decision),
  }
}
