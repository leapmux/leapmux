---
title: "Layout & Windows"
description: "Reference for leapmux tile, window, and layout commands."
type: docs
weight: 4
---

<!-- The command tree generates this page. Do not edit it by hand: change the command, then run
     go test ./cmd/leapmux -run TestControlReference_IsCurrent -update-golden
     in backend/. -->

Every command on this page also takes the [flags of every command](/docs/reference/commands/#flags-of-every-command). See [CLI Guide](/docs/using/cli-guide/) for target selection, output formatting, and authentication.

## Common arguments and flags

These selectors and flags repeat across commands on this page.

| Selector / Flag | Description |
|---|---|
| `<tile>` | A tile id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, or "self" for the tile of the tab that runs this command. |
| `--allow-busy` | close even if work is in progress or the Worker cannot verify the tab's activity |
| `<workspace>` | A workspace id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title, or "self" for the workspace of the tab that runs this command. |

## `leapmux tile`

Split, close and arrange the tiles of a workspace.

### `leapmux tile split`

Split a tile in two; its tabs stay in the first half.

_Mutates state._

```text
leapmux tile split <tile> [flags]
```

| Argument | Description |
|---|---|
| `<tile>` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--direction string` | the divider: "vertical" puts the new tile on the right, "horizontal" puts it below (one of: vertical, horizontal; default: vertical) |

```sh
# Split the tile of this tab, with the new tile on the right.
leapmux tile split self

# Split a tile with the new tile below.
leapmux tile split ab12cd34 --direction horizontal
```

### `leapmux tile close`

Close a tile and give its space to its neighbour.

_Aliases: `rm` · Mutates state._

```text
leapmux tile close <tile> [flags]
```

| Argument | Description |
|---|---|
| `<tile>` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--with-tabs string` | what happens to the tabs on the tile: "close" closes them; "move" moves them to the neighbouring tile; required when the tile has tabs (one of: close, move) |
| `--recursive` | close a split or a grid with every tile in it |
| `--allow-busy` | See [common arguments and flags](#common-arguments-and-flags). |
| `--allow-self` | close the tile even when this tab sits on it, which ends this command's own process |

```sh
leapmux tile close ab12cd34

# Close a tile and move its tabs to the neighbouring tile.
leapmux tile close ab12cd34 --with-tabs move
```

### `leapmux tile make-grid`

Turn a tile into a grid of rows and columns.

_Mutates state._

```text
leapmux tile make-grid <tile> [flags]
```

| Argument | Description |
|---|---|
| `<tile>` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--rows int` | rows of the grid, from 1 to 20 (required) |
| `--cols int` | columns of the grid, from 1 to 20 (required) |

```sh
leapmux tile make-grid ab12cd34 --rows 2 --cols 2
```

### `leapmux tile remove-grid`

Remove a grid and every tile in it.

_Mutates state._

```text
leapmux tile remove-grid <tile> [flags]
```

| Argument | Description |
|---|---|
| `<tile>` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--with-tabs string` | what happens to the tabs in the grid: "close" closes them; "move" turns the grid into one tile that holds them; required when the grid has tabs (one of: close, move) |
| `--allow-busy` | See [common arguments and flags](#common-arguments-and-flags). |
| `--allow-self` | remove the grid even when this tab sits in it, which ends this command's own process |

```sh
# Turn a grid back into one tile that keeps its tabs.
leapmux tile remove-grid ab12cd34 --with-tabs move
```

### `leapmux tile set-ratios`

Set the sizes of the parts of a split.

_Mutates state._

```text
leapmux tile set-ratios <tile> [flags]
```

| Argument | Description |
|---|---|
| `<tile>` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--ratios string` | the size of each part, separated by commas; the CLI scales them to sum to 1 (required) |

```sh
leapmux tile set-ratios ab12cd34 --ratios 0.3,0.7
```

### `leapmux tile set-grid-ratios`

Set the sizes of the rows or the columns of a grid.

_Mutates state._

```text
leapmux tile set-grid-ratios <tile> [flags]
```

| Argument | Description |
|---|---|
| `<tile>` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--row-ratios string` | the size of each row, separated by commas |
| `--col-ratios string` | the size of each column, separated by commas |

```sh
leapmux tile set-grid-ratios ab12cd34 --row-ratios 0.6,0.4
```

### `leapmux tile resize`

Resize a tile inside its parent split to a target ratio (0 &lt; ratio &lt; 1).

_Mutates state._

```text
leapmux tile resize <tile> [flags]
```

| Argument | Description |
|---|---|
| `<tile>` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--ratio number` | new share of the parent split for this tile, between 0 and 1 (exclusive) (required) |

```sh
leapmux tile resize self --ratio 0.65
leapmux tile resize ab12cd34 --ratio 0.3
```

### `leapmux tile swap`

Swap the positions of two tiles in the same workspace.

_Mutates state._

```text
leapmux tile swap <tile-a> <tile-b> [flags]
```

| Argument | Description |
|---|---|
| `<tile-a>` | A tile id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, or "self" for the tile of the tab that runs this command. |
| `<tile-b>` | A tile id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, or "self" for the tile of the tab that runs this command. |

```sh
leapmux tile swap ab12cd34 ef56gh78
leapmux tile swap self ef56gh78
```

## `leapmux window`

List, create, position, resize, and close floating windows.

Aliases: `win`.

### `leapmux window list`

List live floating windows (default: all workspaces).

_Aliases: `ls`._

```text
leapmux window list [workspace] [flags]
```

| Argument | Description |
|---|---|
| `[workspace]` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux window list
leapmux window list self
```

### `leapmux window create`

Create a floating window in a workspace.

_Mutates state._

```text
leapmux window create <workspace> [flags]
```

| Argument | Description |
|---|---|
| `<workspace>` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--x number` | horizontal position in pixels (default: 100) |
| `--y number` | vertical position in pixels (default: 100) |
| `--width number` | width in pixels (must be &gt; 0) (default: 640) |
| `--height number` | height in pixels (must be &gt; 0) (default: 420) |
| `--opacity number` | window opacity from 0.0 to 1.0 (default: 1) |

```sh
leapmux window create self --x 120 --y 80 --width 640 --height 420
```

### `leapmux window move`

Move a floating window to new coordinates or another workspace.

_Aliases: `mv` · Mutates state._

```text
leapmux window move <window> [flags]
```

| Flag | Description |
|---|---|
| `--x number` | new horizontal position in pixels |
| `--y number` | new vertical position in pixels |
| `--workspace string` | move the window to this workspace |

```sh
leapmux window move 7aB1c2d3 --x 200 --y 150
leapmux window move 7aB1c2d3 --workspace "Release 1.4"
```

### `leapmux window resize`

Resize a floating window.

_Mutates state._

```text
leapmux window resize <window> [flags]
```

| Flag | Description |
|---|---|
| `--width number` | new width in pixels (must be &gt; 0) |
| `--height number` | new height in pixels (must be &gt; 0) |

```sh
leapmux window resize 7aB1c2d3 --width 800 --height 600
```

### `leapmux window set-opacity`

Set the opacity of a floating window (0.0 to 1.0).

_Mutates state._

```text
leapmux window set-opacity <window> <opacity> [flags]
```

```sh
leapmux window set-opacity 7aB1c2d3 0.85
```

### `leapmux window close`

Close a floating window and its inner tiles.

_Aliases: `rm` · Mutates state._

```text
leapmux window close <window> [flags]
```

| Flag | Description |
|---|---|
| `--allow-busy` | See [common arguments and flags](#common-arguments-and-flags). |
| `--allow-self` | close the window even when this tab lives in it |

```sh
leapmux window close 7aB1c2d3
```

## `leapmux layout`

Read, replace and watch the whole tile tree of a workspace.

### `leapmux layout get`

Print the tile tree of a workspace with its tabs (default: the workspace of this tab).

```text
leapmux layout get [workspace] [flags]
```

| Argument | Description |
|---|---|
| `[workspace]` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
# Save a layout, and restore it later.
leapmux layout get --json > layout.json
```

### `leapmux layout set`

Replace the main tile tree of a workspace. Main-tree tabs move to the new first tile, and tabs inside floating windows stay in place. The file is "-" or absent for stdin. It holds a tree as "layout get" prints it: the whole --json envelope, its data, or the tree alone.

_Mutates state._

```text
leapmux layout set <workspace> [file] [flags]
```

| Argument | Description |
|---|---|
| `<workspace>` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux layout set self layout.json
leapmux layout get --json | leapmux layout set 9f3kQ2aB -
```

### `leapmux layout watch`

Print every change of your layouts as it happens: the CRDT operations, the presence of other clients, and the creation, renaming and deletion of workspaces. The first event is the snapshot. Without a workspace, it watches every workspace.

_Does not support `-q` / `--quiet`._

```text
leapmux layout watch [workspace] [flags]
```

| Argument | Description |
|---|---|
| `[workspace]` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux layout watch --json
```
