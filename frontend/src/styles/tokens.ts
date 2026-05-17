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
