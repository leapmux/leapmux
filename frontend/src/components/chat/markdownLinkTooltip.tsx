import type { JSX } from 'solid-js'
import type { AnchoredTooltipDismissReason, AnchoredTooltipHandle } from '~/components/common/Tooltip'
import { createEffect, createSignal, onCleanup, Show, untrack } from 'solid-js'
import { createAnchoredTooltip } from '~/components/common/Tooltip'

export interface MarkdownLinkTooltipOptions {
  bodyEl: () => HTMLElement | undefined
  html: () => string | undefined
  enabled: () => boolean
  hasTargets: () => boolean
  localActive: () => boolean
  resolveTarget: (node: EventTarget | null, root: HTMLElement) => HTMLElement | undefined
  isLocalTarget: (target: HTMLElement) => boolean
  getTooltipText: (target: HTMLElement) => string | undefined
}

export interface MarkdownLinkTooltipController {
  onPointerOver: (event: PointerEvent) => void
  onPointerOut: (event: PointerEvent) => void
  onFocusIn: (event: FocusEvent) => void
  onFocusOut: (event: FocusEvent) => void
  dismiss: () => void
  Popover: () => JSX.Element
}

function LinkTooltipHost(props: {
  onHandle: (handle: AnchoredTooltipHandle | undefined) => void
  onDismiss: (reason: AnchoredTooltipDismissReason) => void
}): JSX.Element {
  const handle = createAnchoredTooltip({
    onDismiss: reason => props.onDismiss(reason),
  })
  untrack(() => props.onHandle(handle))
  onCleanup(() => props.onHandle(undefined))
  return <handle.Popover />
}

/**
 * Delegated hover and focus tooltip controller for rendered Markdown links.
 *
 * Mounts {@link createAnchoredTooltip} lazily on the first pointerover or focusin
 * hitting a link target, tracks hover/focus precedence, and keeps tracked state
 * synchronized across HTML swaps, local-link deactivation, and internal tooltip
 * dismissals (click, auxclick, hide timer, or another tooltip taking over).
 */
export function createMarkdownLinkTooltip(
  options: MarkdownLinkTooltipOptions,
): MarkdownLinkTooltipController {
  const [mounted, setMounted] = createSignal(false)
  const active = () => options.enabled() && options.hasTargets()

  let tooltip: AnchoredTooltipHandle | undefined
  let hoveredTarget: HTMLElement | undefined
  let focusedTarget: HTMLElement | undefined
  let shownTarget: HTMLElement | undefined

  const syncTooltip = () => {
    const next = hoveredTarget ?? focusedTarget
    if (next === shownTarget)
      return
    if (!tooltip)
      return
    shownTarget = next
    if (next)
      tooltip.show(next, () => options.getTooltipText(next))
    else
      tooltip.hide()
  }

  const dismiss = () => {
    hoveredTarget = undefined
    focusedTarget = undefined
    shownTarget = undefined
    tooltip?.dismiss()
  }

  const handleInternalDismiss = (reason: AnchoredTooltipDismissReason) => {
    if (reason === 'click') {
      hoveredTarget = undefined
      focusedTarget = undefined
    }
    shownTarget = undefined
  }

  // Drop detached targets on HTML updates and synchronously dismiss any local
  // tooltip (including in-flight HIDE_DELAY_MS tooltips) when local links deactivate
  // while leaving active or hiding remote link tooltips undisturbed.
  let prevHtml: string | undefined
  createEffect(() => {
    const el = options.bodyEl()
    const currentHtml = options.html()
    const localActive = options.localActive()
    const hasTargets = options.hasTargets()

    const htmlReplaced = prevHtml !== undefined && currentHtml !== prevHtml
    prevHtml = currentHtml

    if (!el || !currentHtml || !hasTargets) {
      if (hoveredTarget || focusedTarget || shownTarget || tooltip?.activeTarget())
        dismiss()
      return
    }

    const hoveredDetached = Boolean(hoveredTarget && !hoveredTarget.isConnected)
    const focusedDetached = Boolean(focusedTarget && !focusedTarget.isConnected)
    if (hoveredDetached || focusedDetached || htmlReplaced) {
      if (hoveredDetached)
        hoveredTarget = undefined
      if (focusedDetached)
        focusedTarget = undefined
      shownTarget = undefined
      tooltip?.dismiss()
    }

    if (!localActive) {
      if (hoveredTarget && options.isLocalTarget(hoveredTarget))
        hoveredTarget = undefined
      if (focusedTarget && options.isLocalTarget(focusedTarget))
        focusedTarget = undefined
      const activeTooltipTarget = tooltip?.activeTarget()
      if (activeTooltipTarget && options.isLocalTarget(activeTooltipTarget)) {
        shownTarget = undefined
        tooltip?.dismiss()
        syncTooltip()
      }
    }
  })

  const onPointerOver = (event: PointerEvent) => {
    if (!active() || event.pointerType === 'touch')
      return
    const el = options.bodyEl()
    if (!el)
      return
    const target = options.resolveTarget(event.target, el)
    if (!target)
      return
    if (options.resolveTarget(event.relatedTarget, el) === target)
      return
    hoveredTarget = target
    if (!mounted())
      setMounted(true)
    syncTooltip()
  }

  const onPointerOut = (event: PointerEvent) => {
    if (!hoveredTarget)
      return
    const el = options.bodyEl()
    if (el && options.resolveTarget(event.relatedTarget, el) === hoveredTarget)
      return
    hoveredTarget = undefined
    syncTooltip()
  }

  const onFocusIn = (event: FocusEvent) => {
    if (!active())
      return
    const el = options.bodyEl()
    if (!el)
      return
    const target = options.resolveTarget(event.target, el)
    if (!target)
      return
    focusedTarget = target
    if (!mounted())
      setMounted(true)
    syncTooltip()
  }

  const onFocusOut = (event: FocusEvent) => {
    if (!focusedTarget)
      return
    const el = options.bodyEl()
    if (el && options.resolveTarget(event.relatedTarget, el) === focusedTarget)
      return
    focusedTarget = undefined
    syncTooltip()
  }

  const Popover = (): JSX.Element => (
    <Show when={active() && mounted()}>
      <LinkTooltipHost
        onDismiss={handleInternalDismiss}
        onHandle={(handle) => {
          tooltip = handle
          if (handle) {
            syncTooltip()
          }
          else {
            hoveredTarget = undefined
            focusedTarget = undefined
            shownTarget = undefined
          }
        }}
      />
    </Show>
  )

  return {
    onPointerOver,
    onPointerOut,
    onFocusIn,
    onFocusOut,
    dismiss,
    Popover,
  }
}
