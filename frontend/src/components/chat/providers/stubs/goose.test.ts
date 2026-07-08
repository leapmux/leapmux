import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { providerFor } from '../registry'
import { describeACPStubBasics } from './stubBasics'

import './goose'

const MODE_AUTO = 'auto'

describe('goose provider', () => {
  const plugin = providerFor(AgentProvider.GOOSE)!

  describeACPStubBasics(plugin, { text: true, image: true, pdf: true, binary: true })

  it('uses auto as bypass permission mode', () => {
    expect(plugin.bypassPermissionMode).toBe(MODE_AUTO)
  })

  it('has no plan mode', () => {
    // Goose's writable axis is the top-level permission mode (no plan toggle);
    // the generic settings panel renders the permissionMode group it reports.
    expect(plugin.planMode).toBeUndefined()
  })

  it('still renders the permissionMode group as the trigger mode segment (no plan mode needed)', () => {
    // The trigger mode segment is decoupled from plan mode: Goose has a mode axis
    // (permissionMode) without a plan toggle, so it still declares triggerModeGroupKey.
    expect(plugin.triggerModeGroupKey).toBe('permissionMode')
  })

  it('derives a control-response label via the default ACP permission path (no question hook)', () => {
    // Goose has no question protocol, so it gets the shared acpControlResponseDisplay default.
    expect(plugin.controlResponseDisplay!({
      provider: 'GOOSE',
      requestId: '7',
      request: { method: 'session/request_permission', params: { options: [{ optionId: 'proceed_once', name: 'Allow once' }] } },
      response: { result: { outcome: { optionId: 'proceed_once' } } },
    })).toEqual({ kind: 'label', text: 'Allow once' })
  })
})
