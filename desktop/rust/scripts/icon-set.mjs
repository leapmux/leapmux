// The desktop icon set: one entry for each icon file that tauri-bundler packages.
//
// A file name states the icon's logical size and its density. `128x128.png` is
// 128 physical pixels; `128x128@2x.png` is 256. tauri-bundler reads both facts,
// from two different places: the pixel size comes from the PNG header, and the
// density comes from the `@2x` suffix on the file stem (`utils::is_retina`).
//
// The two lists differ because the two bundlers accept different sizes, and
// neither one reports a size that it cannot use.
//
// `bundle.icon` in tauri.conf.json and tauri.linux.conf.json MUST match these
// lists. icon-set.test.mjs fails when they drift.

// Icons that the Linux .deb and .AppImage ship. tauri-bundler installs each one
// at `usr/share/icons/hicolor/<pixels>x<pixels>[@2]/apps/<binary>.png`.
//
// Every size here is a size that the hicolor theme declares in its index.theme.
// This constraint is the whole reason the list exists: an icon that lands in an
// undeclared directory installs without an error and stays invisible to every
// launcher, because an icon lookup reads index.theme and never sees that
// directory. A 1024x1024 icon named `icon@2x.png` produced exactly that failure
// -- it installed to `hicolor/1024x1024@2/apps/`, which no theme declares, and
// the app showed no icon at all (issue #387).
//
// The order carries one decision, and only the first entry matters: tauri-codegen
// embeds the FIRST PNG of `bundle.icon` as the default window icon, decoded to
// raw RGBA, and GTK publishes that one image as _NET_WM_ICON. 128x128 leads the
// list because a panel scales it down cleanly to the 24 to 64 pixel button it
// draws, and it costs 64 KiB of binary. 32x32 would cost 4 KiB and blur when a
// 48 pixel panel scales it up; the 1024x1024 master would cost 4 MiB. The
// bundler reads every other entry, so their order is free.
export const LINUX_ICON_FILES = [
  '128x128.png',
  '32x32.png',
  '48x48.png',
  '64x64.png',
  '128x128@2x.png',
  '256x256.png',
  '256x256@2x.png',
  '512x512.png',
]

// Icons that macOS and Windows ship.
//
// The macOS bundler packs each PNG into the .icns through
// `icns::IconType::from_pixel_size_and_density`, which maps 16, 32, 48, 64, 128,
// 256, 512, and 1024 pixels only. It resizes anything else down to the next
// power of two, so a 96 would add a duplicate rather than an icon. The 1024
// pixel master (`512x512@2x.png`) is the entry the Dock needs at scale 2, and it
// is also the reason this list stays separate from the Linux one: hicolor
// declares no directory for 1024 pixels.
//
// The master leads the list because `cargo tauri dev` on macOS has no .app
// bundle to take a Dock icon from, so tauri-codegen embeds the first PNG as the
// dev Dock icon. It is the default window icon too, which macOS never draws.
//
// Windows takes its icon from `icon.ico` -- the WiX installer, the executable
// resource, and tauri-codegen's window icon each search `bundle.icon` for the
// first `.ico` -- so the PNGs are inert there and the ICO can stay last.
export const SHARED_ICON_FILES = [
  '512x512@2x.png',
  '32x32.png',
  '128x128.png',
  '128x128@2x.png',
  '256x256.png',
  '256x256@2x.png',
  '512x512.png',
]

// The Windows icon, and the size of the single image inside it.
export const ICO_FILE = 'icon.ico'
export const ICO_SIZE = 256

// The tray / menu-bar icons.
//
// They are NOT bundler icons and must never join the two lists above or
// `bundle.icon` in either config. The binary embeds them with
// `tauri::include_image!`, which decodes the PNG at compile time, so
// tauri-bundler never sees them -- and a tray PNG that reached the Linux
// bundler would install into the hicolor theme as if it were the app icon.
//
// Each entry states its size, because these names do not carry one: the
// `<width>x<height>[@2x].png` form that `pixelSize` parses states a LOGICAL
// size and a density, and a status icon has neither. The platform decides how
// large it draws the image.
//
// `source` picks which SVG renders it. macOS needs the monochrome template
// (see icons/leapmux-icon-mono.svg); the Linux panel and the Windows
// notification area draw the app's own colours.
export const TRAY_ICON_FILES = [
  // Windows draws the notification area at SM_CXSMICON -- 16 pixels at 100% DPI
  // and 32 at 200% -- and the Linux appindicator panel typically at 22 to 24.
  // 32 covers the largest of those without an upscale.
  { name: 'tray.png', size: 32, source: 'colour' },
  // macOS: tray-icon forces the status image to 18 points, so 36 pixels is
  // exactly @2x and nothing is resampled on a Retina display.
  { name: 'tray-template.png', size: 36, source: 'template' },
]

const ICON_NAME_PATTERN = /^(\d+)x(\d+)(@2x)?\.png$/

// Returns the physical pixel size that `name` states. `128x128@2x.png` is 256
// pixels, because `@2x` means two device pixels for each logical pixel.
export function pixelSize(name) {
  const match = ICON_NAME_PATTERN.exec(name)
  if (!match) {
    throw new Error(`Icon name ${name} does not have the form <width>x<height>[@2x].png`)
  }
  const [, width, height, retina] = match
  if (width !== height) {
    throw new Error(`Icon ${name} is not square`)
  }
  return Number(width) * (retina ? 2 : 1)
}

// Returns the path, relative to the package root, where tauri-bundler installs
// `name` for the binary `binaryName`. Mirrors `list_icon_files` in
// tauri-bundler's linux/freedesktop module: the directory carries the PNG's
// pixel dimensions, and `@2` marks the high density variant.
export function hicolorIconPath(name, binaryName) {
  const pixels = pixelSize(name)
  const density = name.includes('@2x') ? '@2' : ''
  return `usr/share/icons/hicolor/${pixels}x${pixels}${density}/apps/${binaryName}.png`
}

// Every PNG that the generator renders, so one build serves each platform's
// list. Declared last because it calls pixelSize while the module evaluates.
export const ALL_ICON_FILES = [...new Set([...LINUX_ICON_FILES, ...SHARED_ICON_FILES])]
  .sort((a, b) => pixelSize(a) - pixelSize(b) || a.localeCompare(b))
