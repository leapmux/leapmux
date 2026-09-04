import type { BootDocumentAsset } from '~/lib/bootDocumentAssets'
import { For, Show } from 'solid-js'
import { getRequestEvent } from 'solid-js/web'
import {
  bootDeferStylesScript,
  deferredStylesheetAttrs,
  partitionBootDocumentAssets,
  resolveClientEntryHref,
} from '~/lib/bootDocumentAssets'

function linkAttrs(asset: BootDocumentAsset, deferStylesheet: boolean): Record<string, unknown> {
  const raw = { ...(asset.attrs ?? {}) }
  delete raw.key
  return deferStylesheet ? deferredStylesheetAttrs(raw) : raw
}

function styleAttrs(asset: BootDocumentAsset): Record<string, unknown> {
  const raw = { ...(asset.attrs ?? {}) }
  delete raw.key
  return raw
}

/**
 * Filtered cold-start head assets. Ignores StartServer's pre-rendered
 * `assets` prop; reads the raw list from the page event instead.
 */
export function BootDocumentHeadAssets() {
  const event = getRequestEvent() as { assets?: BootDocumentAsset[] } | undefined
  const assets = event?.assets ?? []
  const { immediate, deferredStylesheets } = partitionBootDocumentAssets(
    assets,
    resolveClientEntryHref(),
  )

  const immediateLinks = immediate
    .filter(a => a.tag === 'link')
    .map(a => linkAttrs(a, false))
  const immediateStyles = immediate
    .filter(a => a.tag === 'style')
    .map(a => ({ attrs: styleAttrs(a), children: a.children }))
  const deferredLinks = deferredStylesheets.map(a => linkAttrs(a, true))

  return (
    <>
      <For each={immediateLinks}>
        {attrs => <link {...attrs} />}
      </For>
      <For each={immediateStyles}>
        {s => <style {...s.attrs}>{s.children as string | undefined}</style>}
      </For>
      <For each={deferredLinks}>
        {attrs => <link {...attrs} />}
      </For>
      <Show when={deferredLinks.length > 0}>
        <script>{bootDeferStylesScript()}</script>
      </Show>
    </>
  )
}
