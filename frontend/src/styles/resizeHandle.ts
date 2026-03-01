/**
 * Generates `::before` selectors for a resize handle with consistent
 * hover/active visual feedback.
 *
 * @param direction  - `'horizontal'` for col-resize, `'vertical'` for row-resize
 * @param defaultBackground - default `::before` background (default: `'transparent'`)
 * @param prefix - selector prefix (default: `'&'`); use e.g.
 *   `'&[data-direction="horizontal"]'` for attribute-qualified selectors
 */
export function resizeHandleSelectors(
  direction: 'horizontal' | 'vertical',
  defaultBackground = 'transparent',
  prefix = '&',
): Record<string, Record<string, unknown>> {
  const isH = direction === 'horizontal'
  return {
    [`${prefix}::before`]: {
      content: '""',
      position: 'absolute',
      ...(isH
        ? { top: 0, bottom: 0, left: '50%', width: '1px', transform: 'translateX(-50%)' }
        : { left: 0, right: 0, top: '50%', height: '1px', transform: 'translateY(-50%)' }),
      background: defaultBackground,
      transition: 'background 0.15s',
    },
    [`${prefix}:hover::before`]: {
      background: 'var(--border)',
      ...(isH ? { width: '4px' } : { height: '4px' }),
    },
    [`${prefix}:active::before`]: {
      background: 'var(--primary)',
      ...(isH ? { width: '1px' } : { height: '1px' }),
    },
  }
}
