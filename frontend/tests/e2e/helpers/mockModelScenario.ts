import type {
  MockModelRule,
  MockModelScenarioStatus,
  MockModelStep,
} from './mockModelScript'
import { randomUUID } from 'node:crypto'
import { AMBIENT_SCENARIO_ID, SCENARIO_MARKER, validateScenarioID } from './mockModelScript'

/**
 * The test client for the mock model server.
 *
 * The server holds no prompt knowledge, so every provider-specific pattern
 * lives here, named and replaceable. A test that needs its own answer for a
 * housekeeping turn passes its own rules instead of the defaults.
 */

/** The session title the housekeeping rules answer with. */
export const MOCK_SESSION_TITLE = 'LeapMux E2E'

/**
 * The opening of a dedicated title prompt.
 *
 * Every provider that names a session asks for it on its own line, so the
 * pattern anchors there. Without that anchor a coding system prompt that
 * mentions Title Case headings would take the rule and answer a real turn with
 * a session title.
 */
const TITLE_REQUEST = '(?:^|\\n)\\s*(?:(?:generate|create|write|suggest|produce)[^\\n]{0,48}\\btitles?\\b|title-generation task)'

/** A title prompt that asks for a JSON object rather than a bare line. */
const JSON_TITLE_FORM = '(?:valid JSON object|\\{\\s*"title")'

/**
 * Grok Build's session title prompt.
 *
 * Grok titles a session through its own system prompt and FORCES its `session_title`
 * tool through `tool_choice`, so the answer is a call of that tool rather than a
 * line of text. Grok offers no switch for this first title (the later refresh has
 * one, which the E2E environment turns off), and a child session asks for one as
 * well.
 */
const GROK_TITLE_REQUEST = '^You are tasked with generating the session title\\b'

/** The tool Grok forces for a session title, and the argument that carries it. */
const GROK_TITLE_TOOL = 'session_title'

/**
 * Kiro's intent classification.
 *
 * Before a turn in one of its spec modes, Kiro asks a model of its own whether the
 * prompt asks for a spec or for task execution, and it states the question in the
 * request's `agentMode`. The answer is a JSON object of the two confidences. A failed
 * classification falls back to a local guess, but it would consume the step that the
 * test scripted for the turn.
 */
const KIRO_INTENT_CLASSIFICATION = '"agentMode":"intent-classification"'

/**
 * The turns a provider runs for itself.
 *
 * A provider names its session from its own prompt, at a moment the test does
 * not control. Answering those through rules keeps them out of the ordered
 * queue, so a scripted step always belongs to a turn the test sent.
 */
export const HOUSEKEEPING_RULES: readonly MockModelRule[] = [
  {
    name: 'title-json',
    when: { system: [TITLE_REQUEST, JSON_TITLE_FORM] },
    respond: { text: JSON.stringify({ title: MOCK_SESSION_TITLE }) },
  },
  { name: 'title-system', when: { system: TITLE_REQUEST }, respond: { text: MOCK_SESSION_TITLE } },
  { name: 'title-user', when: { user: TITLE_REQUEST }, respond: { text: MOCK_SESSION_TITLE } },
  {
    name: 'title-grok',
    when: { system: GROK_TITLE_REQUEST },
    respond: { toolCalls: [{ id: 'grok-session-title', name: GROK_TITLE_TOOL, arguments: { [GROK_TITLE_TOOL]: MOCK_SESSION_TITLE } }] },
  },
  {
    name: 'intent-kiro',
    when: { protocol: 'aws-event-stream', body: KIRO_INTENT_CLASSIFICATION },
    respond: { text: JSON.stringify({ specGeneration: 0.9, taskExecution: 0.1 }) },
  },
]

/** A script: the ordered queue, the rules that bypass it, or both. */
export interface MockModelScript {
  steps?: MockModelStep[]
  rules?: MockModelRule[]
  /**
   * The answer for a request that outlives the queue.
   *
   * Without one, an unscripted turn is a recorded failure, which is the
   * default. State one only when the turn count is genuinely unknowable.
   */
  fallback?: MockModelStep
  /**
   * Replace the housekeeping rules rather than extend them. Pass an empty array
   * to make every request of the scenario consume a step.
   */
  housekeeping?: MockModelRule[]
}

export interface MockModelScenarioClient {
  id: string
  /** Mark a prompt so its model requests reach this scenario. */
  prompt: (text: string) => string
  /** Read the live consumption state, for a test that asserts on it mid-run. */
  status: () => Promise<MockModelScenarioStatus>
}

type ScriptInput = MockModelStep[] | MockModelScript

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

/** Register one script, run the test body, verify consumption, and remove it. */
export async function withMockModelScenario<T>(
  serverURL: string,
  script: ScriptInput | ((scenario: MockModelScenarioClient) => ScriptInput),
  run: (scenario: MockModelScenarioClient) => Promise<T>,
): Promise<T> {
  const id = `scenario-${randomUUID()}`
  const scenario: MockModelScenarioClient = {
    id,
    prompt: (text: string) => mockScenarioPrompt(id, text),
    status: () => readScenarioStatus(serverURL, id),
  }
  await registerMockModelScenario(serverURL, id, typeof script === 'function' ? script(scenario) : script)

  let result: T
  try {
    result = await run(scenario)
  }
  catch (error) {
    try {
      await removeMockModelScenario(serverURL, id, { force: true })
    }
    catch (cleanupError) {
      throw new AggregateError([error, cleanupError], `Model scenario ${id} and its cleanup failed`)
    }
    throw error
  }

  const status = await removeMockModelScenario(serverURL, id)
  if (!status)
    return result
  try {
    await removeMockModelScenario(serverURL, id, { force: true })
  }
  catch (cleanupError) {
    throw new AggregateError([incompleteScenario(id, status), cleanupError], `Model scenario ${id} verification and cleanup failed`)
  }
  throw incompleteScenario(id, status)
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
 * A step joins the end of the queue. A rule joins the front of the rule list,
 * so it wins over the housekeeping rules that the registration installed.
 */
export async function extendMockModelScenario(serverURL: string, id: string, script: ScriptInput): Promise<void> {
  const declared: MockModelScript = Array.isArray(script) ? { steps: script } : script
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

/**
 * Remove a scenario.
 *
 * Returns the status when the server refused because the script is unconsumed,
 * and undefined when the removal succeeded.
 */
export async function removeMockModelScenario(
  serverURL: string,
  id: string,
  options: { force?: boolean } = {},
): Promise<MockModelScenarioStatus | undefined> {
  const endpoint = scenarioEndpoint(serverURL, id)
  if (options.force)
    endpoint.searchParams.set('force', 'true')
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

/**
 * Everything the mock endpoint saw, for a failure attachment.
 *
 * Reports the read failure as data rather than raising it: this runs while a
 * test already failed, and a second exception there replaces the real cause.
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
 * It holds the housekeeping rules and no steps, so a provider can name a
 * session it started outside a test, and a content turn that no test scripted
 * still fails with the request recorded.
 */
export async function registerAmbientScenario(serverURL: string): Promise<void> {
  await registerMockModelScenario(serverURL, AMBIENT_SCENARIO_ID, { rules: [] })
}

function resolveScript(script: ScriptInput): { steps: MockModelStep[], rules: MockModelRule[] } {
  const declared: MockModelScript = Array.isArray(script) ? { steps: script } : script
  const housekeeping = declared.housekeeping ?? HOUSEKEEPING_RULES
  return {
    steps: declared.steps ?? [],
    // A test rule wins over a housekeeping rule with the same subject, because
    // the server takes the first match in order.
    rules: [...declared.rules ?? [], ...housekeeping],
    ...(declared.fallback ? { fallback: declared.fallback } : {}),
  }
}

function scenarioEndpoint(serverURL: string, id: string): URL {
  return new URL(`/__e2e/scenarios/${encodeURIComponent(id)}`, serverURL)
}

function incompleteScenario(id: string, status: MockModelScenarioStatus): Error {
  const unexpected = status.unexpectedRequests.length
  const summary = `${status.nextStep} of ${status.stepCount} steps consumed, ${unexpected} unexpected request${unexpected === 1 ? '' : 's'}`
  return new Error(`Model scenario ${id} did not consume its complete script: ${summary}.\n${JSON.stringify(status, null, 2)}`)
}
