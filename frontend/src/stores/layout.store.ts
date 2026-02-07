import type { LayoutNode } from '~/generated/leapmux/v1/workspace_pb'
import { create } from '@bufbuild/protobuf'
import { createStore, produce } from 'solid-js/store'
import { LayoutLeafSchema, LayoutNodeSchema, LayoutSplitSchema, SplitDirection } from '~/generated/leapmux/v1/workspace_pb'

// --- Local types (plain JSON, not proto) ---

export interface SplitNode {
  type: 'split'
  id: string
  direction: 'horizontal' | 'vertical'
  ratios: number[]
  children: LayoutNodeLocal[]
}

export interface LeafNode {
  type: 'leaf'
  id: string
}

export type LayoutNodeLocal = SplitNode | LeafNode

interface LayoutStoreState {
  root: LayoutNodeLocal
  focusedTileId: string | null
}

let idCounter = 0
function generateTileId(): string {
  idCounter++
  return `tile-${Date.now()}-${idCounter}`
}

// --- Proto conversion ---

export function fromProto(node: LayoutNode): LayoutNodeLocal {
  if (node.node.case === 'leaf') {
    return { type: 'leaf', id: node.node.value.id }
  }
  if (node.node.case === 'split') {
    const s = node.node.value
    return {
      type: 'split',
      id: s.id,
      direction: s.direction === SplitDirection.VERTICAL ? 'vertical' : 'horizontal',
      ratios: [...s.ratios],
      children: s.children.map(c => fromProto(c)),
    }
  }
  // Fallback: create a default leaf
  return { type: 'leaf', id: generateTileId() }
}

export function toProto(node: LayoutNodeLocal): LayoutNode {
  if (node.type === 'leaf') {
    const leaf = create(LayoutLeafSchema, { id: node.id })
    return create(LayoutNodeSchema, { node: { case: 'leaf' as const, value: leaf } })
  }
  const split = create(LayoutSplitSchema, {
    id: node.id,
    direction: node.direction === 'vertical' ? SplitDirection.VERTICAL : SplitDirection.HORIZONTAL,
    ratios: [...node.ratios],
    children: node.children.map(c => toProto(c)),
  })
  return create(LayoutNodeSchema, { node: { case: 'split' as const, value: split } })
}

// --- Optimization ---

export function optimize(node: LayoutNodeLocal): LayoutNodeLocal {
  if (node.type === 'leaf')
    return node

  // Recursively optimize children first
  const optimizedChildren = node.children.map(c => optimize(c))
  let result: SplitNode = { ...node, children: optimizedChildren }

  // Unwrap single-child split
  if (result.children.length === 1) {
    return result.children[0]
  }

  // Flatten same-direction nesting
  const newChildren: LayoutNodeLocal[] = []
  const newRatios: number[] = []
  let changed = false

  for (let i = 0; i < result.children.length; i++) {
    const child = result.children[i]
    if (child.type === 'split' && child.direction === result.direction) {
      const parentRatio = result.ratios[i]
      for (let j = 0; j < child.children.length; j++) {
        newChildren.push(child.children[j])
        newRatios.push(parentRatio * child.ratios[j])
      }
      changed = true
    }
    else {
      newChildren.push(child)
      newRatios.push(result.ratios[i])
    }
  }

  if (changed) {
    result = { ...result, children: newChildren, ratios: newRatios }
  }

  // After flattening, check if we ended up with a single child
  if (result.children.length === 1) {
    return result.children[0]
  }

  return result
}

// --- Helper: collect all leaf tile IDs ---

export function getAllTileIds(node: LayoutNodeLocal): string[] {
  if (node.type === 'leaf')
    return [node.id]
  return node.children.flatMap(c => getAllTileIds(c))
}

// --- Helper: find and replace a node by tile ID ---

function replaceNode(
  root: LayoutNodeLocal,
  tileId: string,
  replacer: (leaf: LeafNode) => LayoutNodeLocal,
): LayoutNodeLocal {
  if (root.type === 'leaf') {
    return root.id === tileId ? replacer(root) : root
  }
  return {
    ...root,
    children: root.children.map(c => replaceNode(c, tileId, replacer)),
  }
}

// --- Helper: add a sibling to an existing same-direction split ---
// Returns [newRoot, true] if the tile's parent split matches the direction
// and the sibling was added; [root, false] otherwise.

function addSiblingInSameDirectionSplit(
  root: LayoutNodeLocal,
  tileId: string,
  newTileId: string,
  direction: 'horizontal' | 'vertical',
): [LayoutNodeLocal, boolean] {
  if (root.type === 'leaf')
    return [root, false]

  if (root.direction === direction) {
    const childIndex = root.children.findIndex(
      c => c.type === 'leaf' && c.id === tileId,
    )
    if (childIndex >= 0) {
      // Found the tile as a direct child of a same-direction split.
      // Insert the new tile after it and redistribute ratios equally.
      const newChildren = [...root.children]
      newChildren.splice(childIndex + 1, 0, { type: 'leaf', id: newTileId })
      const equalRatio = 1 / newChildren.length
      return [{
        ...root,
        children: newChildren,
        ratios: newChildren.map(() => equalRatio),
      }, true]
    }
  }

  // Recurse into children
  for (let i = 0; i < root.children.length; i++) {
    const [newChild, found] = addSiblingInSameDirectionSplit(
      root.children[i],
      tileId,
      newTileId,
      direction,
    )
    if (found) {
      return [{
        ...root,
        children: root.children.map((c, j) => j === i ? newChild : c),
      }, true]
    }
  }

  return [root, false]
}

function removeNode(
  root: LayoutNodeLocal,
  tileId: string,
): LayoutNodeLocal | null {
  if (root.type === 'leaf') {
    return root.id === tileId ? null : root
  }

  const newChildren: LayoutNodeLocal[] = []
  const newRatios: number[] = []

  for (let i = 0; i < root.children.length; i++) {
    const result = removeNode(root.children[i], tileId)
    if (result !== null) {
      newChildren.push(result)
      newRatios.push(root.ratios[i])
    }
  }

  if (newChildren.length === 0)
    return null
  if (newChildren.length === 1)
    return newChildren[0]

  // Renormalize ratios
  const sum = newRatios.reduce((a, b) => a + b, 0)
  const normalized = newRatios.map(r => r / sum)

  return {
    ...root,
    children: newChildren,
    ratios: normalized,
  }
}

// --- Helper: get depth of a tile in the tree ---

function getTileDepth(node: LayoutNodeLocal, tileId: string, depth: number): number {
  if (node.type === 'leaf') {
    return node.id === tileId ? depth : -1
  }
  for (const child of node.children) {
    const d = getTileDepth(child, tileId, depth + 1)
    if (d >= 0)
      return d
  }
  return -1
}

// --- Store ---

export function createLayoutStore() {
  const defaultTileId = generateTileId()
  const [state, setState] = createStore<LayoutStoreState>({
    root: { type: 'leaf', id: defaultTileId },
    focusedTileId: defaultTileId,
  })

  return {
    state,

    setLayout(node: LayoutNodeLocal) {
      const tileIds = getAllTileIds(node)
      setState('root', node)
      // If focused tile no longer exists, focus the first tile
      if (!tileIds.includes(state.focusedTileId ?? '')) {
        setState('focusedTileId', tileIds[0] ?? null)
      }
    },

    setFocusedTile(tileId: string) {
      setState('focusedTileId', tileId)
    },

    focusedTileId(): string {
      return state.focusedTileId ?? getAllTileIds(state.root)[0] ?? ''
    },

    initSingleTile(): string {
      const tileId = generateTileId()
      setState('root', { type: 'leaf', id: tileId })
      setState('focusedTileId', tileId)
      return tileId
    },

    splitTileHorizontal(tileId: string): string {
      return this._splitTile(tileId, 'horizontal')
    },

    splitTileVertical(tileId: string): string {
      return this._splitTile(tileId, 'vertical')
    },

    _splitTile(tileId: string, direction: 'horizontal' | 'vertical'): string {
      const newTileId = generateTileId()

      // Try to add as a sibling in an existing same-direction split
      const [newRoot, added] = addSiblingInSameDirectionSplit(
        state.root,
        tileId,
        newTileId,
        direction,
      )

      if (added) {
        setState('root', optimize(newRoot))
      }
      else {
        // Create a new split wrapping the tile
        const wrapped = replaceNode(state.root, tileId, leaf => ({
          type: 'split' as const,
          id: generateTileId(),
          direction,
          ratios: [0.5, 0.5],
          children: [
            { type: 'leaf' as const, id: leaf.id },
            { type: 'leaf' as const, id: newTileId },
          ],
        }))
        setState('root', optimize(wrapped))
      }

      return newTileId
    },

    closeTile(tileId: string) {
      const result = removeNode(state.root, tileId)
      if (result) {
        const optimized = optimize(result)
        setState('root', optimized)
        // Update focused tile if needed
        const tileIds = getAllTileIds(optimized)
        if (!tileIds.includes(state.focusedTileId ?? '')) {
          setState('focusedTileId', tileIds[0] ?? null)
        }
      }
    },

    updateRatios(splitId: string, ratios: number[]) {
      setState(produce((s) => {
        const updateInNode = (node: LayoutNodeLocal): void => {
          if (node.type === 'split') {
            if (node.id === splitId) {
              node.ratios = ratios
            }
            else {
              node.children.forEach(updateInNode)
            }
          }
        }
        updateInNode(s.root)
      }))
    },

    canSplitTile(tileId: string): boolean {
      return getTileDepth(state.root, tileId, 0) < 3
    },

    getAllTileIds(): string[] {
      return getAllTileIds(state.root)
    },

    toProto(): LayoutNode {
      return toProto(state.root)
    },

    fromProto(node: LayoutNode) {
      const local = fromProto(node)
      this.setLayout(local)
    },
  }
}
