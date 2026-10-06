import type {
  MockModelRule,
  MockModelScenarioStatus,
  MockModelStep,
} from './mockModelScript'
import { AMBIENT_SCENARIO_ID, isRecord, SCENARIO_MARKER, validateGateName, validateScenarioID } from './mockModelScript'

/**
 * This client registers scripts for the mock model server.
 * It supplies default rules for native housekeeping turns.
 * Provider helpers can replace these defaults with their own rules.
 * The server also accepts provider-owned lifecycle rules through its Surface hooks.
 */

/** The session title the housekeeping rules answer with. */
export const MOCK_SESSION_TITLE = 'LeapMux E2E'

/**
 * Match a dedicated title request at the start of a line.
 * The anchor excludes a system prompt that merely discusses Title Case headings.
 * Without that anchor, a housekeeping answer could replace the test's content answer.
 */
const TITLE_REQUEST = '(?:^|\\n)\\s*(?:(?:generate|create|write|suggest|produce)[^\\n]{0,48}\\btitles?\\b|title-generation task)'

/** A title prompt that asks for a JSON object rather than a bare line. */
const JSON_TITLE_FORM = '(?:valid JSON object|\\{\\s*"title")'

/**
 * Grok requests a session title through its own system prompt.
 * Its tool_choice requires a session_title call, so the response must invoke that tool.
 * Grok offers no switch for the first title request. A child session makes that request also.
 * The E2E environment disables the separate later title refresh.
 */
const GROK_TITLE_REQUEST = '^You are tasked with generating the session title\\b'

/** The tool Grok forces for a session title, and the argument that carries it. */
const GROK_TITLE_TOOL = 'session_title'

/**
 * Kiro's intent classification.
 *
 * Kiro classifies intent before a turn in one of its spec modes.
 * The request's agentMode identifies that classification call.
 * The response supplies confidence values for spec generation and task execution.
 * A failed classification uses a local guess but still consumes a model response.
 * Keep that housekeeping request separate from the test's ordered content steps.
 */
const KIRO_INTENT_CLASSIFICATION = '"agentMode":"intent-classification"'

/**
 * The turns a provider runs for itself.
 *
 * A provider requests a session title at a time that the test does not control.
 * Rules answer those housekeeping requests without consuming ordered steps.
 * Each ordered step then belongs to the content turn that the test sent.
 *
 * Each housekeeping rule has high priority. A title request repeats the prompt of its session, so a test rule that
 * matches the prompt text matches the title request too. Within one priority, a test rule precedes a housekeeping
 * rule (see `resolveScript` here and `extendScenario` in `./mockModelServer`). At normal priority, the test rule would
 * take the title request as a content turn. A test that needs another housekeeping answer adds a high-priority rule:
 * a newer rule of the same priority matches first.
 */
export const HOUSEKEEPING_RULES: readonly MockModelRule[] = [
  {
    name: 'claude-auto-harm',
    priority: 'high',
    when: {
      protocol: 'anthropic-messages',
      system: 'You are a security monitor for autonomous AI coding agents\\.',
      user: 'Respond with <severity>N</severity> ONLY\\.',
    },
    respond: { text: '<severity>0</severity>' },
  },
  {
    name: 'title-json',
    priority: 'high',
    when: { system: [TITLE_REQUEST, JSON_TITLE_FORM] },
    respond: { text: JSON.stringify({ title: MOCK_SESSION_TITLE }) },
  },
  { name: 'title-system', priority: 'high', when: { system: TITLE_REQUEST }, respond: { text: MOCK_SESSION_TITLE } },
  { name: 'title-user', priority: 'high', when: { user: TITLE_REQUEST }, respond: { text: MOCK_SESSION_TITLE } },
  {
    name: 'title-grok',
    priority: 'high',
    when: { system: GROK_TITLE_REQUEST },
    respond: { toolCalls: [{ id: 'grok-session-title', name: GROK_TITLE_TOOL, arguments: { [GROK_TITLE_TOOL]: MOCK_SESSION_TITLE } }] },
  },
  {
    name: 'intent-kiro',
    priority: 'high',
    when: { protocol: 'aws-event-stream', body: KIRO_INTENT_CLASSIFICATION },
    respond: { text: JSON.stringify({ specGeneration: 0.9, taskExecution: 0.1 }) },
  },
]

/**
 * The input that registers or extends a scenario: the ordered queue, the rules that bypass it, or both.
 *
 * It is not the `ModelScript` handle that `modelScriptFixture.ts` gives a test.
 */
export interface MockModelScenarioInput {
  steps?: MockModelStep[]
  rules?: MockModelRule[]
  /**
   * The answer for a request that outlives the queue.
   *
   * An unscripted turn fails by default and retains its request record.
   * Supply a fallback only when the test cannot determine the native turn count.
   */
  fallback?: MockModelStep
  /**
   * Replace the housekeeping rules rather than extend them. Pass an empty array
   * to make every request of the scenario consume a step.
   */
  housekeeping?: MockModelRule[]
}

type ScriptInput = MockModelStep[] | MockModelScenarioInput

/**
 * Put a scenario identifier in a user prompt so a concurrent request finds the
 * correct script.
 *
 * The marker goes on the LAST line. LeapMux derives a subagent registry title
 * and a tab name from the FIRST line of a prompt (`bgtask.FirstLine`), so a
 * leading marker would become that name.
 */
export function mockScenarioPrompt(id: string, prompt: string): string {
  validateScenarioID(id)
  return `${prompt}\n\n${SCENARIO_MARKER}${id}`
}

/** Register a script under a caller-chosen identifier. */
export async function registerMockModelScenario(serverURL: string, id: string, script: ScriptInput): Promise<void> {
  validateScenarioID(id)
  const response = await fetch(scenarioEndpoint(serverURL, id), {
    method: 'PUT',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify(resolveScript(script)),
  })
  if (response.status !== 201)
    throw new Error(`Could not register model scenario ${id}: ${response.status} ${await response.text()}`)
}

/**
 * Add steps or rules to a registered scenario.
 *
 * Append each new step to the ordered queue.
 * Insert each new rule before existing rules of the same priority.
 * Registration installs the default housekeeping rules first.
 */
export async function extendMockModelScenario(serverURL: string, id: string, script: ScriptInput): Promise<void> {
  const declared: MockModelScenarioInput = Array.isArray(script) ? { steps: script } : script
  const response = await fetch(scenarioEndpoint(serverURL, id), {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({
      steps: declared.steps ?? [],
      rules: declared.rules ?? [],
      ...(declared.fallback ? { fallback: declared.fallback } : {}),
    }),
  })
  if (response.status !== 200)
    throw new Error(`Could not extend model scenario ${id}: ${response.status} ${await response.text()}`)
}

/** Choose forced cleanup or an atomic scenario verification policy. */
export type RemoveScenarioOptions
  = | { force: true, allowUnconsumed?: never }
    | { force?: false, allowUnconsumed?: boolean }

/**
 * Remove a scenario.
 * Return its actual status when the server refuses the chosen verification policy.
 * Return undefined after successful removal.
 */
export async function removeMockModelScenario(
  serverURL: string,
  id: string,
  options: RemoveScenarioOptions = {},
): Promise<MockModelScenarioStatus | undefined> {
  if (options.force && options.allowUnconsumed)
    throw new Error('Scenario deletion cannot combine force and allow-unconsumed.')
  const endpoint = scenarioEndpoint(serverURL, id)
  if (options.force)
    endpoint.searchParams.set('force', 'true')
  if (options.allowUnconsumed)
    endpoint.searchParams.set('allow-unconsumed', 'true')
  const response = await fetch(endpoint, { method: 'DELETE' })
  if (response.status === 204)
    return undefined
  if (response.status === 409 && !options.force)
    return await response.json() as MockModelScenarioStatus
  throw new Error(`Could not remove model scenario ${id}: ${response.status} ${await response.text()}`)
}

export async function readScenarioStatus(serverURL: string, id: string): Promise<MockModelScenarioStatus> {
  const response = await fetch(scenarioEndpoint(serverURL, id))
  if (!response.ok)
    throw new Error(`Could not read model scenario ${id}: ${response.status} ${await response.text()}`)
  return await response.json() as MockModelScenarioStatus
}

/** Release a scripted model answer that waits at gate. */
export async function releaseMockModelGate(serverURL: string, id: string, gate: string): Promise<void> {
  validateScenarioID(id)
  validateGateName(gate)
  const endpoint = scenarioEndpoint(serverURL, id)
  endpoint.pathname += `/gates/${encodeURIComponent(gate)}/release`
  const response = await fetch(endpoint, { method: 'POST' })
  if (response.status !== 204)
    throw new Error(`Could not release model gate ${gate}: ${response.status} ${await response.text()}`)
}

/** Release only a currently held response in one atomic scenario operation. */
export async function releaseMockModelGateIfHeld(serverURL: string, id: string, gate: string): Promise<boolean> {
  validateScenarioID(id)
  validateGateName(gate)
  const endpoint = scenarioEndpoint(serverURL, id)
  endpoint.pathname += `/gates/${encodeURIComponent(gate)}/release-if-held`
  const response = await fetch(endpoint, { method: 'POST' })
  if (response.status !== 200)
    throw new Error(`Could not clean up model gate ${gate}: ${response.status} ${await response.text()}`)
  const result: unknown = await response.json()
  if (!isRecord(result) || typeof result.released !== 'boolean')
    throw new Error(`The model gate ${gate} cleanup returned no boolean result.`)
  return result.released
}

/**
 * Everything the mock endpoint saw, for a failure attachment.
 *
 * Return a diagnostic read failure as data.
 * The test already failed, so a second exception must not replace its original cause.
 */
export async function readMockModelDiagnostics(serverURL: string): Promise<Record<string, unknown>> {
  const [requests, ambient] = await Promise.all([
    fetch(new URL('/__e2e/requests', serverURL)).then(response => response.json()).catch(asFailure),
    readScenarioStatus(serverURL, AMBIENT_SCENARIO_ID).catch(asFailure),
  ])
  return { requests, ambient }
}

function asFailure(error: unknown): Record<string, string> {
  return { error: error instanceof Error ? error.message : String(error) }
}

/**
 * Register the scenario that answers a request carrying no marker.
 *
 * This scenario has housekeeping rules and no ordered steps.
 * A provider can request a title for a session that started outside a test.
 * An unscripted content turn still fails and retains its request record.
 */
export async function registerAmbientScenario(serverURL: string): Promise<void> {
  await registerMockModelScenario(serverURL, AMBIENT_SCENARIO_ID, { rules: [] })
}

function resolveScript(script: ScriptInput): { steps: MockModelStep[], rules: MockModelRule[] } {
  const declared: MockModelScenarioInput = Array.isArray(script) ? { steps: script } : script
  const housekeeping = declared.housekeeping ?? HOUSEKEEPING_RULES
  return {
    steps: declared.steps ?? [],
    // A test rule of a priority precedes the housekeeping rules of that priority. The housekeeping rules are high, so
    // only a high test rule precedes them: one that overrides a housekeeping answer, or a native preflight rule that
    // must precede a broad child matcher.
    rules: [...declared.rules ?? [], ...housekeeping],
    ...(declared.fallback ? { fallback: declared.fallback } : {}),
  }
}

function scenarioEndpoint(serverURL: string, id: string): URL {
  return new URL(`/__e2e/scenarios/${encodeURIComponent(id)}`, serverURL)
}
