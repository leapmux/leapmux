---
title: "Git & Files"
description: "Reference for leapmux git, worktree, and file commands."
type: docs
weight: 5
---

<!-- The command tree generates this page. Do not edit it by hand: change the command, then run
     go test ./cmd/leapmux -run TestControlReference_IsCurrent -update-golden
     in backend/. -->

Every command on this page also takes the [flags of every command](/docs/reference/commands/#flags-of-every-command). See [CLI Guide](/docs/using/cli-guide/) for target selection, output formatting, and authentication.

## Common arguments and flags

These selectors and flags repeat across commands on this page.

| Selector / Flag | Description |
|---|---|
| `--dir string` | a directory inside the git working tree (default: $LEAPMUX_CONTROL_WORKING_DIR) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

## `leapmux git`

Read and change the git state of a directory on a worker.

### `leapmux git status`

Show the branch and the changed files of a working tree.

_Does not support `-q` / `--quiet`._

```text
leapmux git status [flags]
```

| Flag | Description |
|---|---|
| `--dir string` | See [common arguments and flags](#common-arguments-and-flags). |
| `--worker string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux git status
leapmux git status --dir ~/src/app --worker name:build-box
```

### `leapmux git read`

Print a file as the last commit or the index holds it. The path is the file's absolute path on the worker; the worker finds its repository.

_Aliases: `cat` · Does not support `-q` / `--quiet`._

```text
leapmux git read <path> [flags]
```

| Flag | Description |
|---|---|
| `--ref string` | the version to read (one of: head, staged; default: head) |
| `--limit int` | the most bytes to read (0 reads the default of 256 KiB) |
| `--worker string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux git read ~/src/app/main.go
leapmux git read ~/src/app/main.go --ref staged
```

### `leapmux git checkout`

Check out a branch.

_Mutates state · Does not support `-q` / `--quiet`._

```text
leapmux git checkout <branch> [flags]
```

| Flag | Description |
|---|---|
| `--dir string` | See [common arguments and flags](#common-arguments-and-flags). |
| `--worker string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux git checkout main

# Check out a remote branch.
leapmux git checkout origin/feature --dir ~/src/app
```

### `leapmux git branch`

List, create, inspect and delete branches.

#### `leapmux git branch list`

List the branches of a repository.

_Aliases: `ls`._

```text
leapmux git branch list [flags]
```

| Flag | Description |
|---|---|
| `--dir string` | See [common arguments and flags](#common-arguments-and-flags). |
| `--worker string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux git branch list
```

#### `leapmux git branch create`

Create a branch and check it out.

_Mutates state · Does not support `-q` / `--quiet`._

```text
leapmux git branch create <name> [flags]
```

| Flag | Description |
|---|---|
| `--dir string` | See [common arguments and flags](#common-arguments-and-flags). |
| `--worker string` | See [common arguments and flags](#common-arguments-and-flags). |
| `--base string` | the branch to start from (default: the current branch) |

```sh
leapmux git branch create fix-login --base main
leapmux git branch create fix-login --base main --dir ~/src/app
```

#### `leapmux git branch delete`

Check out another branch, then delete this one.

_Aliases: `rm` · Mutates state · Does not support `-q` / `--quiet`._

```text
leapmux git branch delete <name> [flags]
```

| Flag | Description |
|---|---|
| `--dir string` | See [common arguments and flags](#common-arguments-and-flags). |
| `--worker string` | See [common arguments and flags](#common-arguments-and-flags). |
| `--switch-to string` | the branch to check out before deleting the current branch (default: current branch if deleting another branch, else main/master) |

```sh
leapmux git branch delete fix-login
leapmux git branch delete fix-login --switch-to main --dir ~/src/app
```

#### `leapmux git branch inspect`

Check whether the current branch or worktree has uncommitted or unpushed work before deletion.

_Does not support `-q` / `--quiet`._

```text
leapmux git branch inspect [branch] [flags]
```

| Flag | Description |
|---|---|
| `--dir string` | See [common arguments and flags](#common-arguments-and-flags). |
| `--worker string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux git branch inspect
leapmux git branch inspect fix-login --dir ~/src/app
```

### `leapmux git push`

Push the current branch, with a work-in-progress commit when the tree has changes.

_Mutates state · Does not support `-q` / `--quiet`._

```text
leapmux git push [flags]
```

| Flag | Description |
|---|---|
| `--dir string` | See [common arguments and flags](#common-arguments-and-flags). |
| `--worker string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux git push
leapmux git push --dir ~/src/app
```

### `leapmux git diff`

Show unstaged, staged, or base-ref git diff for a working tree.

_Does not support `-q` / `--quiet`._

```text
leapmux git diff [path]... [flags]
```

| Flag | Description |
|---|---|
| `--dir string` | See [common arguments and flags](#common-arguments-and-flags). |
| `--worker string` | See [common arguments and flags](#common-arguments-and-flags). |
| `--staged` | show staged changes (--cached) |
| `--base string` | diff against this base ref (such as main or HEAD~1) |
| `--limit int` | the most bytes to read (0 reads the default of 256 KiB) |

```sh
leapmux git diff
leapmux git diff --staged --dir ~/src/app
leapmux git diff --base main backend/cmd/leapmux
```

### `leapmux git log`

Show recent commits of a repository.

```text
leapmux git log [flags]
```

| Flag | Description |
|---|---|
| `--dir string` | See [common arguments and flags](#common-arguments-and-flags). |
| `--worker string` | See [common arguments and flags](#common-arguments-and-flags). |
| `-n, --limit int` | maximum number of commits to show (max 200) (default: 20) |
| `--ref string` | starting ref (default: HEAD) |

```sh
leapmux git log
leapmux git log -n 10 --ref main --dir ~/src/app
```

### `leapmux git commit`

Create a git commit in a working tree.

_Mutates state._

```text
leapmux git commit [flags]
```

| Flag | Description |
|---|---|
| `--dir string` | See [common arguments and flags](#common-arguments-and-flags). |
| `--worker string` | See [common arguments and flags](#common-arguments-and-flags). |
| `-m, --message string` | commit message (required) |
| `-a, --all` | stage all modified and untracked files (git add -A) before committing |

```sh
leapmux git commit -m "Fix timeout handling"
leapmux git commit -a -m "Checkpoint changes" --dir ~/src/app
```

## `leapmux worktree`

List, inspect, create, and remove git worktrees on a worker.

Aliases: `wt`.

### `leapmux worktree list`

List the worktrees of a repository.

_Aliases: `ls`._

```text
leapmux worktree list [flags]
```

| Flag | Description |
|---|---|
| `--dir string` | See [common arguments and flags](#common-arguments-and-flags). |
| `--worker string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux worktree list
leapmux worktree list --dir ~/src/app
```

### `leapmux worktree get`

Use a bare path, path:&lt;path&gt;, branch:&lt;branch&gt;, or self (the default). The worker matches the attached branch in the repository selected by --dir. A local tab can supply the repository directory for a branch on the same worker. An explicit path can select its own repository. Use self to find the calling local tab's checkout.

```text
leapmux worktree get [selector] [flags]
```

| Flag | Description |
|---|---|
| `--dir string` | repository directory on the selected worker (default: $LEAPMUX_CONTROL_WORKING_DIR) |
| `--worker string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux worktree get
leapmux worktree get branch:feature/login --dir ~/src/app
leapmux worktree get /path/to/worktree
```

### `leapmux worktree create`

Create a new linked git worktree on a new branch.

_Mutates state._

```text
leapmux worktree create <branch> [flags]
```

| Flag | Description |
|---|---|
| `--dir string` | repository directory on the worker (default: $LEAPMUX_CONTROL_WORKING_DIR) |
| `--base string` | base branch or commit to branch from (default: current branch) |
| `--path string` | explicit filesystem path for the new worktree (default: worker managed location) |
| `--worker string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux worktree create feat/cli-v2
leapmux worktree create feat/cli-v2 --base main --path ~/wt/cli-v2 --dir ~/src/app
```

### `leapmux worktree remove`

Remove a linked git worktree from disk.

_Aliases: `rm`, `delete` · Mutates state._

```text
leapmux worktree remove <path> [flags]
```

| Flag | Description |
|---|---|
| `--force` | remove even if the worktree contains modified or untracked files |
| `--delete-branch` | also delete the local branch checked out in the removed worktree |
| `--dir string` | repository directory when resolving a branch:&lt;branch&gt; selector (default: $LEAPMUX_CONTROL_WORKING_DIR) |
| `--worker string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux worktree remove ~/wt/cli-v2
leapmux worktree remove ~/wt/cli-v2 --force
```

### `leapmux worktree inspect`

Check whether a worktree is locked, dirty, or has unpushed commits before removal.

_Does not support `-q` / `--quiet`._

```text
leapmux worktree inspect [path] [flags]
```

| Flag | Description |
|---|---|
| `--dir string` | See [common arguments and flags](#common-arguments-and-flags). |
| `--worker string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux worktree inspect
leapmux worktree inspect ~/wt/cli-v2
```

## `leapmux file`

List, read, search, glob, and open files on a worker.

### `leapmux file list`

List a directory (default: the working directory of this tab).

_Aliases: `ls`._

```text
leapmux file list [path] [flags]
```

| Flag | Description |
|---|---|
| `--max-depth int` | merge a chain of directories that each hold one directory, up to this depth |
| `--dirs-only` | list only directories |
| `--from-root string` | also list every directory from this ancestor down to &lt;path&gt; |
| `--worker string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux file list
leapmux file list ~/src/app

# List only the directories, and every directory from the home directory down.
leapmux file list ~/src/app --dirs-only --from-root ~
```

### `leapmux file read`

Print a file.

_Aliases: `cat` · Does not support `-q` / `--quiet`._

```text
leapmux file read <path> [flags]
```

| Flag | Description |
|---|---|
| `--offset int` | the byte to start at |
| `--limit int` | the most bytes to read (0 reads the default of 256 KiB) |
| `--worker string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux file read ~/src/app/go.mod

# Read 4 KiB from byte 1024.
leapmux file read ~/src/app/log.txt --offset 1024 --limit 4096
```

### `leapmux file stat`

Show a file's size, time and permissions.

```text
leapmux file stat <path> [flags]
```

| Flag | Description |
|---|---|
| `--worker string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux file stat ~/src/app/go.mod
```

### `leapmux file roots`

List the filesystem roots of a worker.

```text
leapmux file roots [flags]
```

| Flag | Description |
|---|---|
| `--worker string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux file roots
```

### `leapmux file open`

Open a file in a new tab.

_Mutates state._

```text
leapmux file open <path> [flags]
```

| Flag | Description |
|---|---|
| `--display-mode string` | how a document shows at first (one of: render, source, split) |
| `--view-mode string` | which version of the file shows at first (one of: working, head, staged, unified_diff, split_diff) |
| `--diff-base string` | base git ref or revision for diff view modes |
| `--dir string` | the git context of the tab; outside a LeapMux tab, the directory of the file (default: $LEAPMUX_CONTROL_WORKING_DIR) |
| `--workspace string` | workspace of the new tab (default: the workspace of this tab); the tab lands on its first tile unless --tile says otherwise |
| `--tile string` | tile of the new tab (default: the tile of this tab) |
| `--worker string` | See [common arguments and flags](#common-arguments-and-flags). |
| `--first` | put the new tab first on the destination tile |
| `--last` | put the new tab last on the destination tile (default) |
| `--before string` | put the new tab right before this tab |
| `--after string` | put the new tab right after this tab |
| `--focus` | reveal and focus the new tab in open LeapMux windows |
| `--no-focus` | keep the new tab in the background without focusing it |

```sh
leapmux file open ~/src/app/README.md

# Open a file as its staged diff.
leapmux file open ~/src/app/main.go --view-mode unified_diff
```

### `leapmux file set`

Change a file tab's display mode, view mode, or diff base.

_Mutates state._

```text
leapmux file set <tab> [flags]
```

| Argument | Description |
|---|---|
| `<tab>` | A tab id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for an id or id prefix, "title:&lt;title&gt;" or a bare case-insensitive title, or "self" for the tab that runs this command. |

| Flag | Description |
|---|---|
| `--display-mode string` | how the document renders (one of: render, source, split) |
| `--view-mode string` | which version or diff view of the file shows (one of: working, head, staged, unified_diff, split_diff) |
| `--diff-base string` | base git ref or revision for diff view modes |
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux file set 11aa22bb --view-mode unified_diff --diff-base HEAD
leapmux file set 11aa22bb --display-mode split
```

### `leapmux file search`

Search text files under [path] (default: the working directory of this tab) directly on the worker over E2EE. Binary files, .git, and node_modules are skipped automatically.

_Aliases: `grep`._

```text
leapmux file search <pattern> [path] [flags]
```

| Flag | Description |
|---|---|
| `--glob string` | restrict matches to paths matching this glob (such as "**/*.go") |
| `--regex` | treat &lt;pattern&gt; as an RE2 regular expression |
| `-i, --ignore-case` | match case-insensitively |
| `-n, --limit int` | maximum number of matching lines to return (max 1000) (default: 100) |
| `--worker string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux file search "NewLeaf" /path/to/repo/backend/internal/cli
leapmux file search "TODO|FIXME" --regex --glob "**/*.go" -i
```

### `leapmux file glob`

List relative file paths under [path] (default: the working directory of this tab) matching &lt;pattern&gt; (supports *, ?, [...], and ** multi-directory globs).

_Aliases: `find`._

```text
leapmux file glob <pattern> [path] [flags]
```

| Flag | Description |
|---|---|
| `-n, --limit int` | maximum number of paths to return (max 2000) (default: 200) |
| `--worker string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux file glob "**/*.go"
leapmux file glob "*.proto" /path/to/repo/proto/leapmux/v1
```
