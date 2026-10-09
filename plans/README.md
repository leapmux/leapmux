# Coding-agent feature completion plans

Execute these 13 standalone plans in order on top of the current `HEAD`.
Each plan supplies its scope and prerequisites.
It includes the required file paths and acceptance rules.
Each plan uses repository source without another plan file or an earlier session transcript.

## Execution order

| Order | Standalone plan | Required result |
|---|---|---|
| 01 | [Repair and complete the consolidated source](01-repair-consolidated-work.md) | Complete and verify the consolidated unfinished source. |
| 02 | [Finish and accept Wave 1](02-wave-01.md) | Pass the complete combined Wave 1 checks and native acceptance. |
| 03 | [Complete and accept Muse Code](03-muse-code.md) | Accept all 53 Muse Code feature cells with the real CLI and local mock. |
| 04 | [Complete and accept Wave 2](04-wave-02.md) | Pass every Wave 2 requirement with complete native evidence. |
| 05 | [Complete and accept Wave 3](05-wave-03.md) | Pass every Wave 3 requirement with complete native evidence. |
| 06 | [Complete and accept Wave 4](06-wave-04.md) | Pass every Wave 4 requirement with complete native evidence. |
| 07 | [Complete and accept Wave 5](07-wave-05.md) | Pass every Wave 5 requirement with complete native evidence. |
| 08 | [Complete and accept Wave 6](08-wave-06.md) | Pass every Wave 6 requirement with complete native evidence. |
| 09 | [Complete and accept Wave 7](09-wave-07.md) | Pass every Wave 7 requirement with complete native evidence. |
| 10 | [Complete and accept Wave 8](10-wave-08.md) | Pass every Wave 8 requirement with complete native evidence. |
| 11 | [Complete and accept Wave 9](11-wave-09.md) | Resolve the 12 native-setting gap cells for the first provider group. |
| 12 | [Complete and accept Wave 10](12-wave-10.md) | Resolve the six native-setting gap cells for the second provider group. |
| 13 | [Complete Wave 11 and final acceptance](13-wave-11-final-acceptance.md) | Accept all 1,590 cells and pass every final verification step. |

## Scope and preservation

- Preserve all 199 requirement records and their exact target assignments.
- Close all 23 product gaps and all 18 native-setting gaps.
- Preserve the original 1,316 browser cases through the approved retained cases and assertion merges.
- Accept all 30 providers and 53 features: 1,590 matrix cells.
- Use actual native processes with the local mock model server.
- Require complete same-source browser files with zero retries or skipped cases.

## Starting point

Start each plan from the current `HEAD` after its prerequisites pass.
Inspect the implementation and completed work that `HEAD` already contains.
Determine the remaining work from the current source and matching acceptance evidence.
Each standalone plan includes its prerequisites and required checks.
