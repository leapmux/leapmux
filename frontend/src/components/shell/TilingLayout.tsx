import type { Component, JSX } from 'solid-js'
import type { DragTeardownHandle } from './dragTeardown'
import type { GridAxis, GridNode, LayoutNodeLocal, SplitNode } from '~/stores/layout.store'
import { createMemo, createSignal, Index, Match, Show, Switch } from 'solid-js'
import { shallowEqualArrays } from '~/lib/shallowEqual'
import { createDragTeardownHandle } from './dragTeardown'
import * as styles from './TilingLayout.css'
import { startManagedAxisDrag } from './useTileDragResize'

/**
 * Prefix-sum positions of the inter-cell handles (excludes the trailing
 * boundary). For ratios [0.3, 0.4, 0.3] returns [0.3, 0.7].
 */
function cumulative(ratios: readonly number[]): number[] {
  const out: number[] = []
  let acc = 0
  for (let i = 0; i < ratios.length - 1; i++) {
    acc += ratios[i]
    out.push(acc)
  }
  return out
}

/**
 * Equality predicate for the local drag-preview signals. Skips notifies
 * when the helper produces identical-content arrays (which happens once
 * the drag is pinned at the clamp floor).
 */
function ratiosEqual(a: number[] | null, b: number[] | null): boolean {
  if (a === null || b === null)
    return a === b
  return shallowEqualArrays(a, b)
}

/**
 * Per-axis drag/preview machinery shared between `SplitRenderer` (single
 * axis) and `GridRenderer` (one per axis). Each axis owns its own
 * drag-preview signal, derived `effective` ratios, prefix-sum positions,
 * and `grid-template` string; `startDrag` wires a pointer-down to
 * `startManagedAxisDrag` using the shared container ref + teardown.
 */
function useAxisRatios(
  axis: () => GridAxis,
  getPersistedRatios: () => readonly number[],
  containerRef: () => HTMLElement | undefined,
  dragTeardown: DragTeardownHandle,
  resolveCommit: () => ((final: number[]) => void) | null,
) {
  const [dragRatios, setDragRatios] = createSignal<number[] | null>(null, { equals: ratiosEqual })
  const effective = () => dragRatios() ?? getPersistedRatios()
  const cumulativeRatios = createMemo(() => cumulative(effective()))
  const gridTemplate = createMemo(() => effective().map(r => `minmax(0, ${r}fr)`).join(' '))
  const startDrag = (index: number, e: PointerEvent) => {
    startManagedAxisDrag(axis(), index, e, containerRef(), dragTeardown, () => {
      const commit = resolveCommit()
      if (!commit)
        return null
      return {
        startRatios: getPersistedRatios(),
        setDragRatios,
        commit,
      }
    })
  }
  return { effective, cumulativeRatios, gridTemplate, startDrag }
}

interface TilingCallbacks {
  renderTile: (tileId: string) => JSX.Element
  onRatioChange?: (splitId: string, ratios: number[]) => void
  onGridRatiosChange?: (gridId: string, axis: GridAxis, ratios: number[]) => void
}

interface TilingLayoutProps extends TilingCallbacks {
  root: LayoutNodeLocal
}

interface LayoutNodeRendererProps extends TilingCallbacks {
  node: LayoutNodeLocal
}

interface SplitRendererProps extends TilingCallbacks {
  split: SplitNode
}

interface GridRendererProps extends TilingCallbacks {
  grid: GridNode
}

export const TilingLayout: Component<TilingLayoutProps> = (props) => {
  return (
    <div class={styles.tilingRoot}>
      <LayoutNodeRenderer
        node={props.root}
        renderTile={props.renderTile}
        onRatioChange={props.onRatioChange}
        onGridRatiosChange={props.onGridRatiosChange}
      />
    </div>
  )
}

function LayoutNodeRenderer(props: LayoutNodeRendererProps): JSX.Element {
  const node = () => props.node
  return (
    <Switch>
      <Match when={node().type === 'leaf' ? node() : null}>
        {leaf => (
          // GridRenderer uses `<Index each={cells}>`, which keeps the
          // same DOM slot when the underlying cell record mutates
          // (placeholder leaf -> real materialised leaf as
          // EntityMaterialized frames arrive). The Match is unkeyed
          // so its child render function isn't re-invoked on
          // truthy-to-truthy transitions, and `renderTile(string)`
          // captures the leaf id as a closure constant — so without
          // this Show, the Tile stays bound to its first id forever
          // and lookups like `mainPredicates().get(tileId)` and
          // `tabStore.getTabsForTile(tileId)` keep targeting the
          // placeholder. Keying on leaf().id re-mounts the Tile
          // exactly when the id changes, picking up the freshly
          // installed predicate and tab list.
          <Show when={leaf().id} keyed>
            {id => props.renderTile(id)}
          </Show>
        )}
      </Match>
      <Match when={node().type === 'grid' ? node() as GridNode : null}>
        {grid => (
          <GridRenderer
            grid={grid()}
            renderTile={props.renderTile}
            onRatioChange={props.onRatioChange}
            onGridRatiosChange={props.onGridRatiosChange}
          />
        )}
      </Match>
      <Match when={node().type === 'split' ? node() as SplitNode : null}>
        {split => (
          <SplitRenderer
            split={split()}
            renderTile={props.renderTile}
            onRatioChange={props.onRatioChange}
            onGridRatiosChange={props.onGridRatiosChange}
          />
        )}
      </Match>
    </Switch>
  )
}

function SplitRenderer(props: SplitRendererProps): JSX.Element {
  const s = () => props.split
  // direction names the orientation of the divider line itself:
  // 'vertical' = a vertical divider (`|`) between two side-by-side
  // panes; 'horizontal' = a horizontal divider (`-`) between two
  // stacked panes. isVerticalDivider() drives every axis-dependent
  // branch below — grid template, cell placement, separator
  // orientation — so flipping that one predicate flips the whole
  // render.
  const isVerticalDivider = () => s().direction === 'vertical'

  let containerRef: HTMLDivElement | undefined
  // Cancel any in-flight drag when the split's structure or persisted
  // ratios change — captured ratios would be stale, so abort without
  // committing. During a drag we only mutate the local signal, not the
  // store, so this never fires mid-drag.
  const dragTeardown = createDragTeardownHandle(
    () => [s().id, s().direction, s().ratios, s().children],
  )

  const axis = useAxisRatios(
    () => isVerticalDivider() ? 'col' : 'row',
    () => s().ratios,
    () => containerRef,
    dragTeardown,
    () => {
      const onRatioChange = props.onRatioChange
      if (!onRatioChange)
        return null
      const splitId = s().id
      return final => onRatioChange(splitId, final)
    },
  )

  return (
    <div
      ref={containerRef}
      class={styles.tilingContainer}
      data-split-id={s().id}
      data-direction={s().direction}
      data-testid="tile-split"
      style={{
        [isVerticalDivider() ? 'grid-template-columns' : 'grid-template-rows']: axis.gridTemplate(),
      }}
    >
      {/*
        Index (not For) here: ratio drags update SetNodeRegister(ratios)
        on the SPLIT, which bumps pendingVersion, which makes
        projectedRoot() recompute, which produces a fresh
        `LayoutNodeLocal` tree with fresh child object references each
        frame. `<For>` keys by reference and would unmount + remount
        every child per frame — collapsing every leaf's Tile / TabBar /
        ChatView, which in turn resets the ThinkingIndicator's
        wasVisible closure and re-plays the 300ms expand animation on
        every drop. `<Index>` keys by slot, so the same DOM stays
        mounted and only the reactive accessor `child()` re-reads the
        fresh node. Mirrors the rationale already documented in
        GridRenderer below.
      */}
      <Index each={s().children}>
        {(child, i) => (
          <div
            class={styles.tilingCell}
            style={{
              'grid-column': isVerticalDivider() ? `${i + 1}` : '1',
              'grid-row': isVerticalDivider() ? '1' : `${i + 1}`,
            }}
          >
            <LayoutNodeRenderer
              node={child()}
              renderTile={props.renderTile}
              onRatioChange={props.onRatioChange}
              onGridRatiosChange={props.onGridRatiosChange}
            />
          </div>
        )}
      </Index>
      <Index each={axis.cumulativeRatios()}>
        {(pos, i) => (
          <div
            class={styles.tilingSeparator}
            data-axis={isVerticalDivider() ? 'col' : 'row'}
            data-testid="tile-resize-handle"
            // role="separator" + aria-orientation describes the separator
            // itself: a vertical-divider split has a *vertical* separator
            // between two side-by-side panes. Keyboard resize is
            // intentionally not implemented; revisit if accessibility
            // audits demand it.
            role="separator"
            aria-orientation={isVerticalDivider() ? 'vertical' : 'horizontal'}
            style={{
              [isVerticalDivider() ? 'left' : 'top']: `${pos() * 100}%`,
            }}
            onPointerDown={e => axis.startDrag(i, e)}
          />
        )}
      </Index>
    </div>
  )
}

function GridRenderer(props: GridRendererProps): JSX.Element {
  const g = () => props.grid

  let containerRef: HTMLDivElement | undefined
  // Cancel any in-flight drag when the grid's structure or persisted
  // ratios change — same rationale as in SplitRenderer.
  const dragTeardown = createDragTeardownHandle(
    () => [g().id, g().rows, g().cols, g().rowRatios, g().colRatios, g().cells],
  )

  const makeAxis = (which: GridAxis) => useAxisRatios(
    () => which,
    () => which === 'col' ? g().colRatios : g().rowRatios,
    () => containerRef,
    dragTeardown,
    () => {
      const onGridRatiosChange = props.onGridRatiosChange
      if (!onGridRatiosChange)
        return null
      const gridId = g().id
      return final => onGridRatiosChange(gridId, which, final)
    },
  )
  const colAxis = makeAxis('col')
  const rowAxis = makeAxis('row')

  return (
    <div
      ref={containerRef}
      class={styles.tilingContainer}
      data-grid-id={g().id}
      data-testid="tile-grid"
      style={{
        'grid-template-rows': rowAxis.gridTemplate(),
        'grid-template-columns': colAxis.gridTemplate(),
      }}
    >
      <Index each={g().cells}>
        {(cell, i) => {
          const cols = () => g().cols
          const r = () => Math.floor(i / cols())
          const c = () => i % cols()
          return (
            <div
              class={styles.tilingCell}
              data-grid-row={r()}
              data-grid-col={c()}
              style={{ 'grid-row': r() + 1, 'grid-column': c() + 1 }}
            >
              <LayoutNodeRenderer
                node={cell()}
                renderTile={props.renderTile}
                onRatioChange={props.onRatioChange}
                onGridRatiosChange={props.onGridRatiosChange}
              />
            </div>
          )
        }}
      </Index>
      <Index each={colAxis.cumulativeRatios()}>
        {(pos, i) => (
          <div
            class={styles.tilingSeparator}
            data-axis="col"
            data-index={i}
            data-testid="grid-resize-handle"
            role="separator"
            aria-orientation="vertical"
            style={{ left: `${pos() * 100}%` }}
            onPointerDown={(e) => { colAxis.startDrag(i, e) }}
          />
        )}
      </Index>
      <Index each={rowAxis.cumulativeRatios()}>
        {(pos, i) => (
          <div
            class={styles.tilingSeparator}
            data-axis="row"
            data-index={i}
            data-testid="grid-resize-handle"
            role="separator"
            aria-orientation="horizontal"
            style={{ top: `${pos() * 100}%` }}
            onPointerDown={(e) => { rowAxis.startDrag(i, e) }}
          />
        )}
      </Index>
    </div>
  )
}
