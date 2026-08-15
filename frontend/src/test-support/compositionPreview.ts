/**
 * The composition-preview node the IME layer paints over the cursor
 * (`createCompositionPreview` sets this testid in ~/lib/terminalIme). Shared
 * by the IME suite and the TerminalView suite so the selector cannot drift
 * from the node the production code builds. Returns null when absent, so
 * null-assertion sites and presence assertions share one shape.
 */
export function compositionPreview(root: ParentNode | null | undefined): HTMLElement | null {
  return root?.querySelector<HTMLElement>('[data-testid="terminal-composition-preview"]') ?? null
}
