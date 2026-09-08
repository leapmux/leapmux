import type { PermissionPresetController } from '../providerSettings'
import type { PermissionPresetChoice } from './permissionPresets'
import type { ActionsProps } from './types'
import { Minus } from 'lucide-solid'
import { createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import {
  applyPermissionPreset,
  buildPermissionPill,
  buildSessionPermissionPill,
  createPermissionPresetChoice,
  permissionPillOptions,
  respondThenApplyPermissionPreset,
  sessionPermissionChoice,
} from './permissionPresets'
import { createControlAnswerState } from './types'

const SMART = { sets: { permissionMode: 'auto' } }
const BYPASS = { sets: { permissionMode: 'bypassPermissions' } }

function controller(partial: Partial<PermissionPresetController> = {}): PermissionPresetController {
  return { smart: SMART, bypass: BYPASS, apply: vi.fn(), ...partial }
}

/** A choice state with nothing stored, so it reports the opening choice alone. */
function openingChoice(opening: PermissionPresetChoice) {
  return createPermissionPresetChoice({ answerState: createControlAnswerState() }, () => opening)
}

describe('permissionPillOptions', () => {
  it('offers Unchanged first, then each preset the controller carries', () => {
    // `unspecified` applies no permission change at all, which is what the
    // label says. It is not a "default mode" the agent switches to.
    expect(permissionPillOptions(controller())).toEqual([
      { key: 'unspecified', label: 'Unchanged', icon: Minus },
      { key: 'smart', label: 'Smart' },
      { key: 'bypass', label: 'Bypass' },
    ])
  })

  it('omits a preset the controller does not carry', () => {
    // ZCode and Codex ship no smart preset; their group is Unchanged + Bypass.
    expect(permissionPillOptions(controller({ smart: undefined }))).toEqual([
      { key: 'unspecified', label: 'Unchanged', icon: Minus },
      { key: 'bypass', label: 'Bypass' },
    ])
  })

  it('offers no group without presets', () => {
    expect(permissionPillOptions(undefined)).toBeUndefined()
    expect(permissionPillOptions(controller({ smart: undefined, bypass: undefined }))).toBeUndefined()
  })
})

describe('buildSessionPermissionPill', () => {
  it('requires a handler that can apply the selected preset', () => {
    expect(buildSessionPermissionPill(controller(), openingChoice('smart'))).toBeDefined()
    expect(buildSessionPermissionPill(controller({ apply: undefined }), openingChoice('smart'))).toBeUndefined()
  })
})

describe('buildPermissionPill', () => {
  it('opens on the choice its surface supplies', () => {
    expect(buildPermissionPill(controller(), openingChoice('smart'))!.selected).toBe('smart')
    expect(buildPermissionPill(controller(), openingChoice('bypass'))!.selected).toBe('bypass')
    expect(buildPermissionPill(controller(), openingChoice('unspecified'))!.selected).toBe('unspecified')
  })

  it('clamps an opening choice the group draws no pill for', () => {
    // A plan approval opens on smart whether or not the provider ships one.
    // Codex and ZCode draw Unchanged + Bypass, and Bypass must never arrive as
    // an opening choice, so the group falls back to the pill that applies
    // nothing.
    const pill = buildPermissionPill(controller({ smart: undefined }), openingChoice('smart'))!

    expect(pill.options.map(o => o.key)).toEqual(['unspecified', 'bypass'])
    expect(pill.selected).toBe('unspecified')
  })

  it('follows a moving opening choice until the user picks a pill', () => {
    // The session can switch mode under an open banner, and an untouched group
    // must move with it. The first click pins the choice.
    const props: Pick<ActionsProps, 'answerState'> = { answerState: createControlAnswerState() }
    const [opening, setOpening] = createSignal<PermissionPresetChoice>('unspecified')
    const choice = createPermissionPresetChoice(props, opening)

    expect(buildPermissionPill(controller(), choice)!.selected).toBe('unspecified')
    setOpening('bypass')
    expect(buildPermissionPill(controller(), choice)!.selected).toBe('bypass')

    buildPermissionPill(controller(), choice)!.onSelect('smart')
    setOpening('unspecified')
    expect(buildPermissionPill(controller(), choice)!.selected).toBe('smart')
    expect(props.answerState.choices()).toEqual({ 'control-permissions-pill': 'smart' })
  })

  it('builds nothing without presets', () => {
    expect(buildPermissionPill(undefined, openingChoice('smart'))).toBeUndefined()
  })

  it('clamps a stored choice the catalog no longer offers back to Unchanged', () => {
    // The catalog withdrew smart while the stored choice still says it: the
    // group must report Unchanged, not a selection with no radio to check it.
    const choice = createPermissionPresetChoice(
      { answerState: createControlAnswerState({ choices: { 'control-permissions-pill': 'smart' } }) },
      () => 'unspecified',
    )

    const pill = buildPermissionPill(controller({ smart: undefined }), choice)!

    expect(pill.options.map(o => o.key)).toEqual(['unspecified', 'bypass'])
    expect(pill.selected).toBe('unspecified')
  })
})

describe('applyPermissionPreset', () => {
  it('applies the chosen preset as one complete settings change', async () => {
    const apply = vi.fn()
    await applyPermissionPreset(controller({ apply }), 'bypass')
    expect(apply).toHaveBeenCalledWith({ sets: { permissionMode: 'bypassPermissions' } })

    await applyPermissionPreset(controller({ apply }), 'smart')
    expect(apply).toHaveBeenCalledWith({ sets: { permissionMode: 'auto' } })
  })

  it('applies nothing for Unchanged, a missing handler, a missing controller, or a withdrawn preset', async () => {
    const apply = vi.fn()
    await applyPermissionPreset(controller({ apply }), 'unspecified')
    await applyPermissionPreset(controller({ apply: undefined }), 'bypass')
    await applyPermissionPreset(undefined, 'bypass')
    // The catalog stopped offering smart while the stored choice still says it.
    await applyPermissionPreset(controller({ apply, smart: undefined }), 'smart')
    expect(apply).not.toHaveBeenCalled()
  })
})

describe('respondThenApplyPermissionPreset', () => {
  it('waits for the response before it applies the preset', async () => {
    let finishResponse: () => void = () => {}
    const respond = vi.fn(() => new Promise<void>((resolve) => {
      finishResponse = resolve
    }))
    const apply = vi.fn()

    const result = respondThenApplyPermissionPreset(respond(), controller({ apply }), 'bypass')
    expect(respond).toHaveBeenCalledOnce()
    expect(apply).not.toHaveBeenCalled()

    finishResponse()
    await result
    expect(apply).toHaveBeenCalledWith({ sets: { permissionMode: 'bypassPermissions' } })
  })

  it('does not apply the preset when the response fails', async () => {
    const responseError = new Error('response failed')
    const apply = vi.fn()

    await expect(respondThenApplyPermissionPreset(
      Promise.reject(responseError),
      controller({ apply }),
      'bypass',
    )).rejects.toBe(responseError)
    expect(apply).not.toHaveBeenCalled()
  })
})

describe('sessionPermissionChoice', () => {
  it('reports the preset the session already has on', () => {
    expect(sessionPermissionChoice(controller({ active: 'smart' }))).toBe('smart')
    expect(sessionPermissionChoice(controller({ active: 'bypass' }))).toBe('bypass')
  })

  it('changes nothing when neither preset is on', () => {
    // An ordinary request must never turn a preset ON by itself. It opens on
    // the running one, or on the pill that applies nothing.
    expect(sessionPermissionChoice(controller())).toBe('unspecified')
    expect(sessionPermissionChoice(undefined)).toBe('unspecified')
  })
})
