import type { BootDocumentAsset } from '~/lib/bootDocumentAssets'
import { For, Show } from 'solid-js'
import { getRequestEvent } from 'solid-js/web'
import {
  bootDeferStylesScript,
  deferredStylesheetAttrs,
  partitionBootDocumentAssets,
  resolveClientEntryHref,
} from '~/lib/bootDocumentAssets'

function attrsWithoutKey(asset: BootDocumentAsset): Record<string, unknown> {
  const raw = { ...(asset.attrs ?? {}) }
  delete raw.key
  return raw
}

function linkAttrs(asset: BootDocumentAsset, deferStylesheet: boolean): Record<string, unknown> {
  const raw = attrsWithoutKey(asset)
  return deferStylesheet ? deferredStylesheetAttrs(raw) : raw
}

/**
 * Filtered cold-start head assets. Ignores StartServer's pre-rendered
 * `assets` prop; reads the raw list from the page event instead.
 *
 * Emits `link`, `style`, and `script` (DEV Vite client and plugin tags).
 * Other tags stay dropped — Vinxi's SPA path only puts those three in
 * `context.assets` today.
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
    .map(a => ({ attrs: attrsWithoutKey(a), children: a.children }))
  const immediateScripts = immediate
    .filter(a => a.tag === 'script')
    .map(a => ({ attrs: attrsWithoutKey(a), children: a.children }))
  const deferredLinks = deferredStylesheets.map(a => linkAttrs(a, true))

  return (
    <>
      <For each={immediateLinks}>
        {attrs => <link {...attrs} />}
      </For>
      <For each={immediateStyles}>
        {s => <style {...s.attrs}>{s.children as string | undefined}</style>}
      </For>
      <For each={immediateScripts}>
        {s => <script {...s.attrs}>{s.children as string | undefined}</script>}
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
