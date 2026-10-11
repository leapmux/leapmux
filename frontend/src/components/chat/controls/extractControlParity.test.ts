import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { clineApprovalRequest } from '~/test-support/clineFixtures'
import { copilotPermissionRequest } from '~/test-support/copilotFixtures'
import { kimiApprovalRequest } from '~/test-support/kimiFixtures'
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
    [AgentProvider.GROK_BUILD, { method: 'session/request_permission', params: { toolCall: { toolCallId: 'c', kind: 'execute', title: 'Execute', rawInput: { variant: 'Bash', command: COMMAND }, _meta: { 'x.ai/tool': { name: 'run_terminal_command' } } } } }],
    [AgentProvider.QWEN_CODE, { method: 'session/request_permission', params: { toolCall: { toolCallId: 'c', kind: 'execute', title: 'Shell', rawInput: { command: COMMAND }, _meta: { toolName: 'run_shell_command' } } } }],
    // Kiro's request states the call as its title alone, and the command in its own metadata.
    [AgentProvider.KIRO, { method: 'session/request_permission', params: { toolCall: { toolCallId: 'run_command_c', status: 'pending', title: COMMAND }, _meta: { kiro: { toolId: 'run_command', command: COMMAND } } } }],
    [AgentProvider.GITHUB_COPILOT, copilotPermissionRequest({ kind: 'shell', intention: 'Run', fullCommandText: COMMAND, commands: [], canOfferSessionApproval: true })],
    [AgentProvider.KIMI_CODE, kimiApprovalRequest('Bash', { kind: 'command', command: COMMAND, cwd: '/work', language: 'bash' })],
    [AgentProvider.OH_MY_PI, { type: 'extension_ui_request', id: 'r', method: 'select', title: `Allow tool: bash\nCommand: ${COMMAND}`, options: ['Approve', 'Deny'] }],
    [AgentProvider.MIMO_CODE, { type: 'permission.asked', properties: { id: 'per_1', sessionID: 's', permission: 'bash', patterns: [COMMAND], metadata: {} }, request: { tool_name: 'bash' } }],
    [AgentProvider.AMP, { type: 'leapmux_amp_permission', tool_name: 'shell_command', tool_use_id: 'TU-1', input: { command: COMMAND, workdir: '/work' } }],
    [AgentProvider.CLINE, clineApprovalRequest('run_commands', { commands: [COMMAND] })],
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
    [AgentProvider.KIMI_CODE, kimiApprovalRequest('ExitPlanMode', { kind: 'plan_review', plan: '# Plan' })],
    // Grok sends `null` for an empty plan file.
    [AgentProvider.GROK_BUILD, { method: '_x.ai/exit_plan_mode', params: { toolCallId: 'c', planContent: null } }],
    [AgentProvider.MIMO_CODE, { type: 'question.asked', properties: { id: 'que_1', sessionID: 's', questions: [{ key: 'plan_exit', params: { plan: 'plan.md' } }] }, request: { tool_name: 'plan_exit' } }],
    // Cline's plan tool asks for approval before it runs; the plan is the answer above it.
    [AgentProvider.CLINE, clineApprovalRequest('switch_to_act_mode', {})],
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
    [AgentProvider.GROK_BUILD, { method: '_x.ai/exit_plan_mode', params: { toolCallId: 'c', planContent: '# Ship it' } }],
    [AgentProvider.QWEN_CODE, { method: 'session/request_permission', params: { options: [], toolCall: { toolCallId: 'c', kind: 'switch_mode', rawInput: { plan: '# Ship it' }, _meta: { toolName: 'exit_plan_mode' } } } }],
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

  it('states an empty list for a provider that offers none', () => {
    const surface = surfaceOf(AgentProvider.AMP, { type: 'leapmux_amp_permission', tool_name: 'apply_patch', input: { patchText: '*** Begin Patch\n*** End Patch' } })
    if (surface?.kind !== 'permission')
      throw new Error('a tool approval is a permission')
    expect(surface.permission.options).toEqual([])
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
    [AgentProvider.GROK_BUILD, { params: { toolCall, options: wireOptions } }],
    [AgentProvider.QWEN_CODE, { params: { toolCall, options: wireOptions } }],
    [AgentProvider.KIRO, { params: { toolCall, options: wireOptions } }],
    // OpenCode and Kilo answer with their own two ids even when the request states none.
    [AgentProvider.OPENCODE, { params: { toolCall } }],
    [AgentProvider.KILO, { params: { toolCall } }],
    // Copilot states no list, so LeapMux states the decisions the runtime accepts.
    [AgentProvider.GITHUB_COPILOT, copilotPermissionRequest({ kind: 'read', canOfferSessionApproval: true })],
    // Kimi Code states no list either: every approval takes the same three answers.
    [AgentProvider.KIMI_CODE, kimiApprovalRequest('Write', { kind: 'file_io', operation: 'write', path: '/a.ts' })],
    // omp states its two answers as the dialog's own options.
    [AgentProvider.OH_MY_PI, { type: 'extension_ui_request', id: 'r', method: 'select', title: 'Allow tool: write\nPath: a.ts', options: ['Approve', 'Deny'] }],
    // MiMo states no list either, and always takes one of its own three reply words.
    [AgentProvider.MIMO_CODE, { type: 'permission.asked', properties: { id: 'per_1', sessionID: 's', permission: 'read', patterns: ['/a.ts'], metadata: {} }, request: { tool_name: 'read' } }],
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
    [AgentProvider.AMP, { type: 'leapmux_amp_permission', tool_name: 'apply_patch', input: { patchText: '*** Begin Patch\n*** End Patch' } }, []],
    [AgentProvider.CLINE, clineApprovalRequest('editor', { path: '/a.ts', old_text: 'a', new_text: 'b' }), []],
  ])('provider %s exposes its supported replies for the request', (provider, payload, options) => {
    const surface = surfaceOf(provider, payload)
    if (surface?.kind !== 'permission')
      throw new Error('a tool approval is a permission')
    expect(surface.permission.options).toEqual(options)
    if (options.length > 0)
      expect(pluginFor(provider)?.controls?.sendPermissionOption).toBeDefined()
  })
})
