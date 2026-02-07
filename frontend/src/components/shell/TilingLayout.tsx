import type { Component, JSX } from 'solid-js'
import type { LayoutNodeLocal, SplitNode } from '~/stores/layout.store'
import Resizable from '@corvu/resizable'
import { For, Show } from 'solid-js'
import * as styles from './TilingLayout.css'

interface TilingLayoutProps {
  root: LayoutNodeLocal
  renderTile: (tileId: string) => JSX.Element
  onRatioChange?: (splitId: string, ratios: number[]) => void
}

interface LayoutNodeRendererProps {
  node: LayoutNodeLocal
  renderTile: (tileId: string) => JSX.Element
  onRatioChange?: (splitId: string, ratios: number[]) => void
}

interface SplitRendererProps {
  split: SplitNode
  renderTile: (tileId: string) => JSX.Element
  onRatioChange?: (splitId: string, ratios: number[]) => void
}

export const TilingLayout: Component<TilingLayoutProps> = (props) => {
  return (
    <div class={styles.tilingRoot}>
      <LayoutNodeRenderer
        node={props.root}
        renderTile={props.renderTile}
        onRatioChange={props.onRatioChange}
      />
    </div>
  )
}

function LayoutNodeRenderer(props: LayoutNodeRendererProps): JSX.Element {
  return (
    <Show
      when={props.node.type === 'split'}
      fallback={props.renderTile((props.node as { id: string }).id)}
    >
      <SplitRenderer
        split={props.node as SplitNode}
        renderTile={props.renderTile}
        onRatioChange={props.onRatioChange}
      />
    </Show>
  )
}

function SplitRenderer(props: SplitRendererProps): JSX.Element {
  const s = () => props.split

  // Key by split id + children identities to force Resizable remount when
  // panels are added, removed, or replaced (e.g. leaf → nested split).
  // This avoids @corvu/resizable's internal size tracking getting out of
  // sync during panel registration/unregistration.
  const splitKey = () => {
    const childKeys = s().children.map(c => c.id).join(',')
    return `${s().id}:${childKeys}`
  }

  return (
    <Show when={splitKey()} keyed>
      {(_key) => {
        // Snapshot the panel structure at render time so that <For> iterates
        // a static array. This prevents @corvu/resizable from seeing
        // incremental panel changes (which corrupt its internal sizes).
        // When the split structure changes, splitKey changes and the entire
        // Resizable is torn down and recreated.
        const snapshotChildren = [...s().children]
        const snapshotRatios = [...s().ratios]
        const snapshotDirection = s().direction
        const snapshotId = s().id
        const childCount = snapshotChildren.length

        const handleSizesChange = (sizes: number[]) => {
          if (!props.onRatioChange)
            return
          if (sizes.length !== childCount)
            return
          const total = sizes.reduce((a, b) => a + b, 0)
          if (Math.abs(total - 1) > 0.01)
            return
          props.onRatioChange(snapshotId, sizes)
        }

        return (
          <Resizable
            orientation={snapshotDirection}
            onSizesChange={handleSizesChange}
          >
            <For each={snapshotChildren}>
              {(_child, index) => (
                <>
                  <Show when={index() > 0}>
                    <Resizable.Handle
                      as="div"
                      class={styles.tileResizeHandle}
                      data-direction={snapshotDirection}
                      data-testid="tile-resize-handle"
                    />
                  </Show>
                  <Resizable.Panel
                    initialSize={snapshotRatios[index()] ?? 1 / childCount}
                    minSize={0.05}
                  >
                    {/* Read child from the live store so nested updates are
                        visible even though <For> iterates a static array. */}
                    <LayoutNodeRenderer
                      node={s().children[index()]}
                      renderTile={props.renderTile}
                      onRatioChange={props.onRatioChange}
                    />
                  </Resizable.Panel>
                </>
              )}
            </For>
          </Resizable>
        )
      }}
    </Show>
  )
}
