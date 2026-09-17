import type { ControlRequestIR } from '../ir/controlRequest'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { copilotPermissionRequest } from '~/test-support/copilotFixtures'
import { pluginFor } from '../providers/registry'
import { controlSurface } from './controlSurface'
import '../providers'

/**
 * One control request per provider, read through the shared derivation.
 *
 * Every provider used to ship a `ControlContent` component that dispatched to the
 * same five bodies, and each decided for itself which fields to pass. The five
 * drifted: a permission on one agent drew its reason and the same permission on the
 * next drew none, because nobody could see the two side by side. This reads them
 * side by side.
 */
function surfaceOf(provider: AgentProvider, payload: Record<string, unknown>): ControlRequestIR | undefined {
  return controlSurface({ requestId: 'r', agentId: 'a', payload }, provider, undefined) as ControlRequestIR | undefined
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
    [AgentProvider.GITHUB_COPILOT, copilotPermissionRequest({ kind: 'shell', intention: 'Run', fullCommandText: COMMAND, commands: [], canOfferSessionApproval: true })],
  ]

  it.each(cases)('provider %s states the command it wants to run', (provider, payload) => {
    const surface = surfaceOf(provider, payload)
    expect(surface?.kind).toBe('permission')
    if (surface?.kind !== 'permission')
      throw new Error('a shell approval is a permission')
    expect(surface.permission.command).toBe(COMMAND)
  })

  // The tool NAME is what a reader weighs first, and every provider states one --
  // under `tool_name`, under a method, or inside the tool call.
  it.each(cases)('provider %s names the operation', (provider, payload) => {
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
  ])('provider %s reads a plan', (provider, payload) => {
    expect(surfaceOf(provider, payload)?.kind).toBe('plan')
  })

  // Copilot is the one provider that sends the WHOLE plan in its request, where the
  // others send an approval that identifies a plan the transcript already holds.
  it('carries the plan text when the request itself holds it', () => {
    const surface = surfaceOf(
      AgentProvider.GITHUB_COPILOT,
      { method: 'session.event', params: { sessionId: 's', event: { id: 'e', type: 'exit_plan_mode.requested', agentId: '', data: { planContent: '# Ship it' } } } },
    )
    expect(surface).toEqual({ kind: 'plan', text: '# Ship it' })
  })
})

describe('the permission options a provider offers', () => {
  // The Agent Client Protocol family and Copilot send their own option lists, which
  // `layoutPermissionOptions` lays out. Every other provider sends none, and the
  // shared Allow/Deny pair answers instead -- an EMPTY list is that statement.
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
    const surface = surfaceOf(AgentProvider.CLAUDE_CODE, { request: { tool_name: 'Read', input: { file_path: '/a.ts' } } })
    if (surface?.kind !== 'permission')
      throw new Error('a tool approval is a permission')
    expect(surface.permission.options).toEqual([])
  })
})

// Pi's extension dialogs are neither a permission nor a question: there is nothing to
// approve and no option list, so they take a variant of their own.
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

  // Pi's own question predicate claims `input` and `select` first -- both offer the
  // reader a CHOICE, which is what the shared question form answers. The dialog
  // control therefore sees `confirm` and `editor` alone, and the IR keeps its
  // `input` variant because Pi's four methods are what it models.
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
 * A provider LeapMux cannot read at all still draws a DECISION: the shared
 * Allow/Deny pair, which is the same answer an unnamed tool takes in the transcript.
 *
 * It states no arguments, and that is the point. The fallback used to invent
 * `input: request.payload`, which is the whole JSON-RPC envelope, so the banner
 * headed it "Arguments" while the Allow button beside it sent
 * `payload.request.input ?? {}` -- the two halves of one banner read two different
 * parts of the payload, and the half the reader saw was not the half the agent got.
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
 * Who answers a permission, and with what.
 *
 * A permission is answered in one of two ways, and which one is not a choice the
 * banner makes: the RUNTIME decides. An agent that states its own option list is
 * answered by picking one of THOSE, and the id travels in that agent's own envelope,
 * so the plugin must state a sender beside the options. An agent that states none is
 * answered by the shared Allow/Deny pair, which needs no sender at all.
 *
 * The two lists below are that partition, and each case carries the payload it
 * classifies -- a plugin that stopped filling its options, or one that added options
 * without a sender, fails here rather than drawing buttons that answer nothing.
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
  ])('provider %s states its own answers and how to send one', (provider, payload) => {
    const surface = surfaceOf(provider, payload)
    if (surface?.kind !== 'permission')
      throw new Error('a tool approval is a permission')
    expect(surface.permission.options.length).toBeGreaterThan(0)
    expect(pluginFor(provider)?.controls?.sendPermissionOption).toBeDefined()
  })

  it.each([
    [AgentProvider.CLAUDE_CODE, { request: { tool_name: 'Read', input: { file_path: '/a.ts' } } }],
    [AgentProvider.ZCODE, { request: { tool_name: 'Read', input: { file_path: '/a.ts' } } }],
    [AgentProvider.CODEX, { method: 'item/commandExecution/requestApproval', params: { command: 'pwd' } }],
  ])('provider %s leaves the shared Allow / Deny pair to answer', (provider, payload) => {
    const surface = surfaceOf(provider, payload)
    if (surface?.kind !== 'permission')
      throw new Error('a tool approval is a permission')
    expect(surface.permission.options).toEqual([])
  })
})
