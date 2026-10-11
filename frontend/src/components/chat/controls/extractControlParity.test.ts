import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { copilotPermissionRequest } from '~/test-support/copilotFixtures'
import { pluginFor } from '../providers/registry'
import { controlSurface } from './controlSurface'
import '../providers'

/**
 * Read one control request per provider through the shared derivation.
 * Each old ControlContent component selected its own fields for the same five bodies.
 * Their outputs differed: one provider displayed its reason while another omitted it.
 * These tests compare those outputs directly.
 */
function surfaceOf(provider: AgentProvider, payload: Record<string, unknown>) {
  return controlSurface({ requestId: 'r', agentId: 'a', payload }, provider, undefined)
}

const COMMAND = 'npm test -- --runInBand'

describe('every provider reads a shell permission the same way', () => {
  // Each payload is that provider's own wire shape for "may I run this command".
  const cases: [AgentProvider, Record<string, unknown>][] = [
    [AgentProvider.CLAUDE_CODE, { request: { tool_name: 'Bash', input: { command: COMMAND } } }],
    [AgentProvider.ZCODE, { request: { tool_name: 'Bash', input: { command: COMMAND } } }],
    [AgentProvider.CODEX, { method: 'item/commandExecution/requestApproval', params: { command: COMMAND } }],
    [AgentProvider.OPENCODE, { params: { toolCall: { toolCallId: 'c', kind: 'execute', title: 'Run', rawInput: { command: COMMAND } } } }],
    [AgentProvider.GOOSE, { params: { toolCall: { toolCallId: 'c', kind: 'execute', title: 'Run', rawInput: { command: COMMAND } } } }],
    // Grok presents the normalized input with a `variant` tag, beside the name in `_meta`.

    // Kiro's request states the call as its title alone, and the command in its own metadata.

    [AgentProvider.GITHUB_COPILOT, copilotPermissionRequest({ kind: 'shell', intention: 'Run', fullCommandText: COMMAND, commands: [], canOfferSessionApproval: true })],

  ]

  it.each(cases)('provider %s states the command it wants to run', (provider, payload) => {
    const surface = surfaceOf(provider, payload)
    expect(surface?.kind).toBe('permission')
    if (surface?.kind !== 'permission')
      throw new Error('a shell approval is a permission')
    expect(surface.permission.command).toBe(COMMAND)
  })

  // Each provider identifies its operation through one of these fields:
  // - A tool name.
  // - A method.
  // - A tool-call field.
  it.each(cases)('provider %s identifies the operation', (provider, payload) => {
    const surface = surfaceOf(provider, payload)
    if (surface?.kind !== 'permission')
      throw new Error('a shell approval is a permission')
    expect(surface.permission.title).toBeTruthy()
  })
})

describe('every provider reads its own plan approval', () => {
  it.each([
    [AgentProvider.CLAUDE_CODE, { request: { tool_name: 'ExitPlanMode', input: {} } }],
    [AgentProvider.ZCODE, { request: { tool_name: 'ExitPlanMode', input: {} } }],
    [AgentProvider.CODEX, { request: { tool_name: 'CodexPlanModePrompt', input: {} } }],

    // Grok sends `null` for an empty plan file.

    // Cline's plan tool asks for approval before it runs; the plan is the answer above it.

  ])('provider %s reads a plan', (provider, payload) => {
    expect(surfaceOf(provider, payload)?.kind).toBe('plan')
  })

  // Copilot sends the plan text in its request.
  // Other requests can identify a plan that the transcript already holds.
  it('carries the plan text when the request itself holds it', () => {
    const surface = surfaceOf(
      AgentProvider.GITHUB_COPILOT,
      { method: 'session.event', params: { sessionId: 's', event: { id: 'e', type: 'exit_plan_mode.requested', agentId: '', data: { planContent: '# Ship it' } } } },
    )
    expect(surface).toEqual({ kind: 'plan', text: '# Ship it' })
  })

  // Grok's own request and Qwen's permission request both carry the plan as well.
  it.each([

  ])('carries the plan text of provider %s', (provider, payload) => {
    expect(surfaceOf(provider, payload)).toEqual({ kind: 'plan', text: '# Ship it' })
  })
})

describe('the permission options a provider offers', () => {
  // An option list can come from the native request or the replies that the provider accepts.
  // An empty list selects the shared Allow/Deny pair.
  it('carries the wire options of a provider that sends them', () => {
    const surface = surfaceOf(AgentProvider.GOOSE, {
      params: {
        toolCall: { toolCallId: 'c', kind: 'other', title: 'Run' },
        options: [{ optionId: 'once', kind: 'allow_once', name: 'Allow once' }],
      },
    })
    if (surface?.kind !== 'permission')
      throw new Error('a tool approval is a permission')
    expect(surface.permission.options).toEqual([{ optionId: 'once', kind: 'allow_once', name: 'Allow once' }])
  })
})

// Pi's confirm and editor dialogs use their own variants instead of the permission or question form.
describe('pi extension dialogs', () => {
  it.each([
    ['confirm', 'confirm'],
    ['editor', 'editor'],
  ])('reads a %s dialog as the %s control', (method, variant) => {
    const surface = surfaceOf(AgentProvider.PI, { type: 'extension_ui_request', method, title: 'Approve?' })
    if (surface?.kind !== 'dialog')
      throw new Error('an extension dialog is a dialog')
    expect(surface.dialog.variant).toBe(variant)
    expect(surface.dialog.title).toBe('Approve?')
  })

  // Pi's question predicate claims input and select first. The shared question form answers both.
  // The dialog control therefore reads confirm and editor alone.
  // The model retains its input variant to describe all four native methods.
  it.each(['input', 'select'])('routes a %s dialog to the question form', (method) => {
    const surface = surfaceOf(AgentProvider.PI, { type: 'extension_ui_request', method, title: 'Approve?' })
    expect(surface?.kind).toBe('question')
  })

  it('states a deadline only when the runtime set one', () => {
    const withTimeout = surfaceOf(AgentProvider.PI, { type: 'extension_ui_request', method: 'confirm', title: 'T', timeout: 30000 })
    const without = surfaceOf(AgentProvider.PI, { type: 'extension_ui_request', method: 'confirm', title: 'T', timeout: 0 })
    if (withTimeout?.kind !== 'dialog' || without?.kind !== 'dialog')
      throw new Error('an extension dialog is a dialog')
    expect(withTimeout.dialog.timeoutMs).toBe(30000)
    expect(without.dialog.timeoutMs).toBeUndefined()
  })
})

/*
 * An unknown provider still gets the shared Allow/Deny pair, as an unnamed transcript tool does.
 * The fallback states no arguments.
 * The old fallback displayed the whole JSON-RPC envelope as Arguments.
 * Its Allow button sent payload.request.input instead, or an empty object when input was absent.
 * The banner therefore displayed data that the agent did not receive.
 */
describe('a payload no provider reads', () => {
  it('falls back to an empty permission rather than nothing', () => {
    expect(controlSurface({ requestId: 'r', agentId: 'a', payload: {} }, undefined, undefined))
      .toEqual({ kind: 'permission', permission: { options: [] } })
  })

  it('invents no arguments out of the envelope it could not read', () => {
    const surface = controlSurface(
      { requestId: 'r', agentId: 'a', payload: { jsonrpc: '2.0', id: 7, method: 'unknown/method', request: { input: { command: 'pwd' } } } },
      undefined,
      undefined,
    )
    expect(surface).toEqual({ kind: 'permission', permission: { options: [] } })
  })
})

/**
 * The native runtime determines the supported permission replies.
 * A provider that exposes option replies must supply their sender also.
 * The selected option ID travels in the native envelope.
 * A request without option replies uses the shared Allow/Deny pair and requires no option sender.
 * Each case supplies the payload that it classifies to detect missing options or senders.
 */
describe('who answers a permission', () => {
  const toolCall = { toolCallId: 'c', kind: 'other', title: 'Write' }
  const wireOptions = [
    { optionId: 'once', kind: 'allow_once', name: 'Allow once' },
    { optionId: 'reject', kind: 'reject_once', name: 'Reject' },
  ]

  it.each([
    // The Agent Client Protocol family sends its options on the request itself.
    [AgentProvider.GOOSE, { params: { toolCall, options: wireOptions } }],
    [AgentProvider.REASONIX, { params: { toolCall, options: wireOptions } }],
    [AgentProvider.CURSOR, { params: { toolCall, options: wireOptions } }],

    // OpenCode and Kilo answer with their own two ids even when the request states none.
    [AgentProvider.OPENCODE, { params: { toolCall } }],
    [AgentProvider.KILO, { params: { toolCall } }],
    // Copilot states no list, so LeapMux states the decisions the runtime accepts.
    [AgentProvider.GITHUB_COPILOT, copilotPermissionRequest({ kind: 'read', canOfferSessionApproval: true })],
    // Kimi Code states no list either: every approval takes the same three answers.

    // omp states its two answers as the dialog's own options.

    // MiMo states no list either, and always takes one of its own three reply words.

  ])('provider %s states its own answers and how to send one', (provider, payload) => {
    const surface = surfaceOf(provider, payload)
    if (surface?.kind !== 'permission')
      throw new Error('a tool approval is a permission')
    expect(surface.permission.options.length).toBeGreaterThan(0)
    expect(pluginFor(provider)?.controls?.sendPermissionOption).toBeDefined()
  })

  it.each([
    [AgentProvider.CLAUDE_CODE, { request: { tool_name: 'Read', input: { file_path: '/a.ts' } } }, [
      { optionId: 'once', kind: 'allow_once', name: 'Allow once' },
      { optionId: 'session', kind: 'allow_always', name: 'Allow for this session' },
      { optionId: 'deny', kind: 'reject_once', name: 'Deny' },
    ]],
    [AgentProvider.ZCODE, { request: { tool_name: 'Read', input: { file_path: '/a.ts' } } }, []],
    [AgentProvider.CODEX, { method: 'item/commandExecution/requestApproval', params: { command: 'pwd' } }, []],

  ])('provider %s exposes its supported replies for the request', (provider, payload, options) => {
    const surface = surfaceOf(provider, payload)
    if (surface?.kind !== 'permission')
      throw new Error('a tool approval is a permission')
    expect(surface.permission.options).toEqual(options)
    if (options.length > 0)
      expect(pluginFor(provider)?.controls?.sendPermissionOption).toBeDefined()
  })
})
