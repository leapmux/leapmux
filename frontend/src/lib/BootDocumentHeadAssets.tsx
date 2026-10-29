import type { BootDocumentAsset } from '~/lib/bootDocumentAssets'
import { createMemo, For, Show } from 'solid-js'
import {
  bootDeferStylesScript,
  deferredStylesheetAttrs,
  partitionBootDocumentAssets,
} from '~/lib/bootDocumentAssets'

function attrsWithoutKey(asset: BootDocumentAsset): Record<string, unknown> {
  const raw = {
    ...(asset.attrs ?? {}),
  }
  delete raw.key
  return raw
}

function linkAttrs(asset: BootDocumentAsset, deferStylesheet: boolean): Record<string, unknown> {
  const raw = attrsWithoutKey(asset)
  return deferStylesheet
    ? deferredStylesheetAttrs(raw)
    : raw
}

/**
 * Filtered cold-start head assets, rendered from the raw list that
 * `takeBootDocumentAssets` took off the page event.
 *
 * Emits `link`, `style`, and `script` (DEV Vite client and plugin tags).
 * Other tags stay dropped — SolidStart's SPA path only puts those three in
 * the page event's `assets` today.
 */
export function BootDocumentHeadAssets(props: { assets: readonly BootDocumentAsset[] }) {
  const partition = createMemo(() => partitionBootDocumentAssets(props.assets))
  const immediate = (tag: string) => partition().immediate.filter(a => a.tag === tag)

  const immediateLinks = () => immediate('link').map(a => linkAttrs(a, false))
  const immediateStyles = () => immediate('style')
    .map(a => ({
      attrs: attrsWithoutKey(a),
      children: a.children,
    }))
  const immediateScripts = () => immediate('script')
    .map(a => ({
      attrs: attrsWithoutKey(a),
      children: a.children,
    }))
  const deferredLinks = () => partition().deferredStylesheets.map(a => linkAttrs(a, true))

  return (
    <>
      <For each={immediateLinks()}>
        {attrs => <link {...attrs} />}
      </For>
      <For each={immediateStyles()}>
        {s => <style {...s.attrs}>{s.children as string | undefined}</style>}
      </For>
      <For each={immediateScripts()}>
        {s => <script {...s.attrs}>{s.children as string | undefined}</script>}
      </For>
      <For each={deferredLinks()}>
        {attrs => <link {...attrs} />}
      </For>
      <Show when={deferredLinks().length > 0}>
        <script>{bootDeferStylesScript()}</script>
      </Show>
    </>
  )
}
