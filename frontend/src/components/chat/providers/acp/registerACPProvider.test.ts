import type { NotificationEntry } from '../../model/notification'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '~/lib/jsonPick'
import { providerFor, registerProvider } from '../registry'
import { input } from '../testUtils'
import { createACPProvider, registerACPProvider } from './registerACPProvider'

const attachmentCapabilities = {
  text: true,
  image: true,
  pdf: true,
  audio: false,
  binary: true,
}

describe('createACPProvider', () => {
  it('connects native notifications to classification and the context meter', () => {
    const nativeNotifications = {
      isMember: (message: Record<string, unknown>) => message.compacted === true,
      entries: (message: Record<string, unknown>): NotificationEntry[] => message.compacted === true
        ? [
            { kind: 'compaction', phase: 'end' },
          ]
        : [],
      compactionBoundary: (message: Record<string, unknown>) => message.compacted === true
        ? { post: 0 }
        : null,
    }
    const options = { attachments: attachmentCapabilities, defaultPermissionMode: 'default' as const, nativeNotifications }
    const plugin = createACPProvider(options)
    const frame = { compacted: true }
    expect(plugin.transcript.notificationEntry).toBe(nativeNotifications.entries)
    expect(plugin.transcript.classify(input(frame))).toEqual({
      kind: 'notification',
      entries: [
        { kind: 'compaction', phase: 'end' },
      ],
    })
    expect(plugin.session?.compactionBoundaryFromMessage?.(input(frame))).toEqual({ post: 0 })
    expect(plugin.session?.compactionBoundaryFromMessage?.(input({ compacted: false }))).toBeNull()
    expect(plugin.session?.compactionBoundaryFromMessage?.(input())).toBeNull()
  })

  it('omits native notification hooks when the provider supplies none', () => {
    const plugin = createACPProvider({ attachments: attachmentCapabilities, defaultPermissionMode: 'default' })
    expect(plugin.transcript.notificationEntry).toBeUndefined()
    expect(plugin.session?.compactionBoundaryFromMessage).toBeUndefined()
  })
  it('forwards the provider path capability without wrapping or parsing it', () => {
    const outputFilePaths = vi.fn(() => ['/native/reported.log'])
    const options = { attachments: attachmentCapabilities, defaultPermissionMode: 'default' as const, outputFilePaths }

    const plugin = createACPProvider(options)

    expect(plugin.transcript.outputFilePaths).toBe(outputFilePaths)
    expect(outputFilePaths).not.toHaveBeenCalled()
  })

  it('omits the path capability when the provider supplies none', () => {
    const plugin = createACPProvider({ attachments: attachmentCapabilities, defaultPermissionMode: 'default' })
    expect(plugin.transcript.outputFilePaths).toBeUndefined()
  })

  it('constructs a captured base without registration and permits one composed registration', () => {
    const base = createACPProvider({ attachments: attachmentCapabilities, defaultPermissionMode: 'default', planValue: 'plan' })
    expect(providerFor(AgentProvider.COMMAND_CODE)).toBeUndefined()
    const extractDivider: typeof base.transcript.extractDivider = (...args) => isObject(args[0]) && args[0].method === 'native/end'
      ? { label: 'Native turn ended' }
      : base.transcript.extractDivider(...args)
    const composed = {
      ...base,
      transcript: { ...base.transcript, extractDivider },
    }
    registerProvider(AgentProvider.COMMAND_CODE, composed)
    expect(providerFor(AgentProvider.COMMAND_CODE)).toBe(composed)
    expect(composed.transcript.extractDivider({ method: 'native/end' })).toEqual({ label: 'Native turn ended' })
    expect(composed.transcript.extractDivider({})).toEqual(base.transcript.extractDivider({}))
    expect(composed.configuration?.triggerModeGroupKey).toBe('permissionMode')
    expect(() => registerProvider(AgentProvider.COMMAND_CODE, composed)).toThrow('already registered')
  })
})

describe('registerACPProvider', () => {
  it('rejects conflicting declarations of the mode group before registration', () => {
    expect(() => registerACPProvider({
      attachments: attachmentCapabilities,
      provider: AgentProvider.DEEPSEEK_HARNESS,
      settingsConfig: { kind: 'permissionMode', defaultMode: 'default' },
      defaultPermissionMode: 'normal',
    })).toThrow('cannot combine settingsConfig and defaultPermissionMode')
    expect(providerFor(AgentProvider.DEEPSEEK_HARNESS)).toBeUndefined()
  })

  // settingsConfig and defaultPermissionMode are the two ways to declare a provider's
  // settings shape; supplying NEITHER is a registration-time misconfiguration that must
  // fail loudly (before registerProvider) rather than register a provider with no axis.
  it('rejects a registration with neither settingsConfig nor defaultPermissionMode', () => {
    expect(() => registerACPProvider({
      attachments: attachmentCapabilities,
      provider: AgentProvider.REASONIX,
    })).toThrow(/settingsConfig or defaultPermissionMode/)
  })

  // The options a provider with its own shapes passes: the display of its saved
  // answers, its permission presets, and its own turn end. Each must reach the plugin
  // it registers, and the control capability holds nothing beside the two.
  it('carries the optional hooks into the plugin it registers', () => {
    const controlResponseDisplay = vi.fn(() => ({ kind: 'label' as const, text: 'Vendor answer' }))
    const permissionPresets = {
      bypass: {
        sets: { approvalMode: 'always-approve' },
      },
    }
    registerACPProvider({
      attachments: attachmentCapabilities,
      provider: AgentProvider.GROK_BUILD,
      defaultPermissionMode: 'default',
      controlResponseDisplay,
      permissionPresets,
      agentTurnEnd: parent => parent.method === 'vendor/end'
        ? 'end_turn'
        : undefined,
    })
    const plugin = providerFor(AgentProvider.GROK_BUILD)!
    expect(plugin.controls).toStrictEqual({ controlResponseDisplay, permissionPresets })
    expect(controlResponseDisplay).not.toHaveBeenCalled()
    expect(plugin.transcript.extractDivider({ method: 'vendor/end' })).toEqual({ label: 'Turn ended' })
  })

  // A provider that passes no display takes the shared permission display, which reads
  // the option that the reader chose from the request's own list.
  it('keeps the shared defaults when a provider passes none', () => {
    registerACPProvider({ attachments: attachmentCapabilities, provider: AgentProvider.REASONIX, defaultPermissionMode: 'normal' })
    const plugin = providerFor(AgentProvider.REASONIX)!
    expect(Object.keys(plugin.controls ?? {})).toEqual(['controlResponseDisplay'])
    expect(plugin.controls?.controlResponseDisplay?.({
      requestId: 'r',
      claimToken: 'c',
      request: {
        method: 'session/request_permission',
        params: {
          options: [
            { optionId: 'no', name: 'No', kind: 'reject_once' },
          ],
        },
      },
      response: {
        result: {
          outcome: { outcome: 'selected', optionId: 'no' },
        },
      },
    })).toEqual({ kind: 'label', text: 'No' })
  })
})
