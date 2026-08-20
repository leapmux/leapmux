import type { ThemeVariant } from '~/styles/themes'

/**
 * The selector a LIGHT variant's rule is emitted under.
 *
 * Two halves, and both are load-bearing:
 *
 *   - `[data-ui-light="id"]` is the SELF match, for `<html>`, which carries the
 *     variant attribute itself.
 *   - `[data-ui-light="id"] [data-theme="light"]` is the DESCENDANT match, for a
 *     subtree pinned to a polarity of its own — a light terminal inside a dark
 *     app. Light used to mean "the attribute is absent", so such a subtree still
 *     painted the dark chrome around it.
 *
 * NOT scoped to `html`. That would raise the self-match's specificity above the
 * descendant match and silently kill subtree theming.
 *
 * A PLAIN MODULE, not a `.css.ts`. Two stylesheets emit this shape —
 * `~/styles/global.css.ts` for the palette and
 * `~/components/chat/widgets/SpanLines.css.ts` for the span rails — and they
 * were tied only by a comment saying they matched. The specificity argument is
 * what makes subtree theming work, so a fix applied to one loop must not be
 * able to leave the other painting the wrong theme.
 *
 * KEEP THE LIGHT LOOP BEFORE THE DARK ONE at every call site. On `<html>`, which
 * carries both attributes, the dark rule wins over the light one only because it
 * is declared second.
 */
export function lightVariantSelector(variant: ThemeVariant): string {
  return `[data-ui-light="${variant.id}"], [data-ui-light="${variant.id}"] [data-theme="light"]`
}

/**
 * The selector a DARK variant's rule is emitted under.
 *
 * The self match carries `[data-theme="dark"]` where the light one does not,
 * because dark is the polarity that must be stated: an element with
 * `data-ui-dark` set but `data-theme` light is a dark VARIANT choice showing its
 * light side, and it must not take these values. See `lightVariantSelector`.
 */
export function darkVariantSelector(variant: ThemeVariant): string {
  return `[data-ui-dark="${variant.id}"][data-theme="dark"], [data-ui-dark="${variant.id}"] [data-theme="dark"]`
}
