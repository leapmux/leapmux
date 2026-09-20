import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { describeACPProviderBasics } from '../acp/testUtils'
import { providerFor } from '../registry'
import './plugin'

const MODE_AUTO = 'auto'
const MODE_SMART_APPROVE = 'smart_approve'

describe('goose provider', () => {
  const plugin = providerFor(AgentProvider.GOOSE)!

  describeACPProviderBasics(AgentProvider.GOOSE, { text: true, image: true, pdf: true, binary: true })

  it('maps smart and bypass permissions to Goose modes', () => {
    expect(plugin?.controls?.permissionPresets).toEqual({
      smart: { sets: { permissionMode: MODE_SMART_APPROVE } },
      bypass: { sets: { permissionMode: MODE_AUTO } },
    })
  })

  it('has no plan mode', () => {
    // Goose's writable axis is the top-level permission mode (no plan toggle);
    // the generic settings panel renders the permissionMode group it reports.
    expect(plugin?.configuration?.planMode).toBeUndefined()
  })

  it('still renders the permissionMode group as the trigger mode segment (no plan mode needed)', () => {
    // The trigger mode segment is decoupled from plan mode: Goose has a mode axis
    // (permissionMode) without a plan toggle, so it still declares triggerModeGroupKey.
    expect(plugin?.configuration?.triggerModeGroupKey).toBe('permissionMode')
  })

  it('derives a control-response label via the default ACP permission path (no question hook)', () => {
    // Goose has no question protocol, so it gets the shared acpControlResponseSummary default.
    expect(plugin?.controls?.controlResponseDisplay!({
      claimToken: 'claim-1',
      requestId: '7',
      request: { method: 'session/request_permission', params: { options: [{ optionId: 'proceed_once', name: 'Allow once' }] } },
      response: { result: { outcome: { optionId: 'proceed_once' } } },
    })).toEqual({ kind: 'label', text: 'Allow once' })
  })
})
