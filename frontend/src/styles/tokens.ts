export const iconSize = {
  xxs: 10,
  xs: 12,
  sm: 14,
  md: 16,
  lg: 18,
  xl: 24,
  container: {
    sm: '20px',
    md: '24px',
    lg: '28px',
  },
}

export const headerHeightPx = 34
export const headerHeight = `${headerHeightPx}px`

// Motion durations in milliseconds. Use `motion.X` in JS (timers,
// tests) and `${motion.X}ms` in vanilla-extract CSS template strings,
// so the JS timeout and the CSS animation can never drift apart.
//
//   fast      — popover / dialog fade, small chrome animations.
//   medium    — drawer slide, overlay fade, panel resize.
//   longPress — the touch hold that opens a row's context menu. The
//               press indicator's ramp IS this duration, so the tint
//               reaching full and the menu becoming ready are the same
//               number by construction. 500ms is what iOS
//               (`UILongPressGestureRecognizer.minimumPressDuration`),
//               Android (`ViewConfiguration.getLongPressTimeout()`),
//               Blink and WebKit all use for a touch long press.
export const motion = {
  fast: 150,
  medium: 200,
  longPress: 500,
}

// Min-width thresholds in CSS pixels, matching Tailwind's defaults.
// Use the complement (`${breakpoints.sm - 1}px`) inside `max-width`
// queries — vanilla-extract evaluates the template at build time so
// the emitted CSS still hits `max-width: 639px`.
//
//   sm — phone form factor. Below `sm`: iOS auto-zoom suppression,
//         dialogs expanding to full viewport, multi-column layouts
//         collapsing to stacks. NOT the same as the mobile-layout
//         switch; phones are always inside the mobile-layout band,
//         but the mobile-layout band also covers small tablets where
//         these phone-specific tweaks would over-reach.
//
//   md  — mobile-layout flavor cutoff. Below `md`,
//         `useIsMobileLayout()` returns true and `AppShell` renders
//         the drawer-based `MobileShellLayer` instead of the tiling
//         `DesktopShellLayer`.
export const breakpoints = {
  sm: 640,
  md: 768,
}

// The compact action button: a queue row's icon actions, the queue row's Steer
// button, and the pause banner's Resume button beside them.
//
// `compactActionSize` is a CONTROL size, not a step on the spacing scale, so no
// `--space-N` token fits it. It is in `rem`, so it follows the root font size
// the way the text in the same row does.
//
// `compactTextSize` is the queue's metadata type size. It sits between
// `--text-8` (0.75rem) and nothing smaller, so no `--text-N` token matches it
// either. Named here because two components repeat it five times.
export const compactActionSize = '1.75rem'
export const compactTextSize = '0.72rem'

// The geometry that every compact action button shares.
//
// A stylesheet SPREADS this into its own `style()` call. It does not compose a
// shared CLASS, because two unlayered classes from two stylesheets tie on
// specificity, and the emission order of the bundler then decides which
// `padding` wins. The `controlReset` comment in `~/styles/shared.css.ts`
// records the rule that this obeys.
export const compactAction = {
  height: compactActionSize,
  flexShrink: 0,
} as const

// The compact action button that also shows a word.
export const compactActionLabelled = {
  ...compactAction,
  padding: `0 var(--space-2)`,
  fontSize: compactTextSize,
} as const

// The composer column's container-query name.
//
// `inputArea` in `~/components/chat/ChatView.css.ts` declares the container;
// `hideInNarrowComposer` in `~/styles/shared.css.ts` and the input queue's own
// rules query it. The composer sits inside a resizable tile and a floating
// window, either of which can be a fraction of the viewport, so a viewport
// media query answers a question about the wrong box: a 260px composer on a
// 1200px display kept rendering "Pause Queue / Interrupt / Send" in full and
// crowded the `[+]` button.
export const composerContainer = 'composer'
