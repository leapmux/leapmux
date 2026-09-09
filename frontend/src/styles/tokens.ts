export const iconSize = {
  xxs: 10,
  xs: 12,
  sm: 14,
  md: 16,
  lg: 18,
  xl: 24,
  // The square an icon BUTTON occupies. `IconButton.css.ts` reads every one of
  // these, and `sharedTree.css.ts` reserves a sidebar row's height from `md`.
  container: {
    sm: '20px',
    md: '24px',
    lg: '28px',
    xl: '36px',
  },
}

export const headerHeightPx = 34
export const headerHeight = `${headerHeightPx}px`

// The body's box, stated once for the TWO stylesheets that paint it: the
// inline boot stylesheet in `~/lib/bootSplashTheme.ts`, which owns the
// document until the app bundle lands, and `~/styles/global.css.ts`, which
// owns it afterwards. The two must agree, or the handoff moves the page.
//
//   bodyHeight         The region the browser shows. `--vvh` is the
//                      keyboard-aware height `~/hooks/useVisualViewportInset`
//                      publishes on `<html>`; `100dvh` answers until then,
//                      which covers the whole pre-JS window.
//   bodySafeAreaTop    The band the body reserves for the system status bar.
//                      Non-zero in the iOS standalone PWA (47px on the iPhone
//                      the E2E fixture emulates) and 0 in a browser tab, on
//                      Android and on a desktop — which is why a disagreement
//                      about it is invisible off that one platform.
//   bodyContentHeight  The body's CONTENT box, because the body pairs the
//                      padding above with `box-sizing: border-box`. `#app`
//                      fills exactly this. A box that states `100dvh` instead
//                      is TALLER than `#app` by the inset: `#app` clips the
//                      overflow at the bottom, and a centred column inside it
//                      sits half the inset too low.
export const bodyHeight = 'var(--vvh, 100dvh)'
export const bodySafeAreaTop = 'env(safe-area-inset-top,0px)'
export const bodyContentHeight = `calc(${bodyHeight} - ${bodySafeAreaTop})`

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

// The scrollbar box, in pixels, and the single source of truth for it.
//
// Three places used to write this number: the `::-webkit-scrollbar` rules in
// `~/styles/global.css.ts`, the xterm overview-ruler width in `~/lib/terminal`
// (which sets the inline width of xterm's own `.slider`), and the inset and
// radius of the slider rules in
// `~/components/terminal/TerminalView.css.ts`. All three asserted in prose that
// they had to match. They agree by construction now.
//
// For the terminal this ONE number does two jobs, because xterm 6 reads
// `options.overviewRuler?.width` in two places and defaults both to 14.
// `FitAddon` subtracts it from the width before dividing by the cell width, so
// it is the gutter the last column stops at. The viewport passes it to the
// vendored VS Code scrollable element as `verticalScrollbarSize`, which is the
// inline width of the `.slider` that element draws -- xterm 6 renders its OWN
// scrollbar rather than leaning on the browser's, so this is the real bar and
// not a reservation for one.
export const scrollbarWidthPx = 8

// The transparent border that insets the thumb inside its box, so the visible
// bar is thinner than the hit target.
export const scrollbarThumbInsetPx = 2

// The thumb itself, as a spreadable declaration.
//
// Shared so the terminal's slider and every other scrollbar in the app are ONE
// shape. Lives here rather than in `~/styles/global.css.ts` because that module
// registers global rules as a side effect, and a `.css.ts` file that imported
// the object from there would pull the whole global stylesheet in with it.
//
// The colours are custom properties on purpose: they are relative-colour
// functions that only a browser resolves, which is why xterm's own
// `scrollbarSlider*` theme entries cannot carry them.
export const scrollbarThumb = {
  backgroundColor: 'var(--scrollbar-thumb)',
  backgroundClip: 'content-box',
  border: `${scrollbarThumbInsetPx}px solid transparent`,
  borderRadius: `${scrollbarWidthPx / 2}px`,
} as const

export const scrollbarThumbHover = {
  backgroundColor: 'var(--scrollbar-thumb-hover)',
} as const
