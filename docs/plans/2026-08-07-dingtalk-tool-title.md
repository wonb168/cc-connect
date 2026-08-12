# DingTalk Tool Title (transient tool-call titles on AI Card)

**Date:** 2026-08-07

**Goal:** Bring DingTalk's AI Card tool-call rendering in line with Feishu's rich-card
behavior: consecutive same-name tool calls collapse into one "Name ×N" title row, no
input/output detail is shown, the tool area clears once body text starts, and the
finalized card carries no tool rows at all. Eliminates the current one-message-per-
tool-event spam.

## Background

Two independent rendering pipelines exist in this codebase for "show tool calls during
a turn," and they share no code:

- **Feishu** (`platform/feishu`, fork-owned): `core.RichCardSupporter` +
  `ToolStep`/`appendToolStep`/`mergeRichToolResult`/`finalToolSteps` in `core/engine.go`,
  gated by `[display] tool_title` (`e.toolTitleTransient`). Already shipped, covered by
  `tests/release_local/turn_contract`.
- **DingTalk** (`platform/dingtalk`, upstream-owned) + **Slack** (`platform/slack`,
  upstream-owned): `core.StreamingCardPlatform` + `cardToolEntry`/`buildCardContent` in
  `core/engine.go`. Every `EventToolUse` appends a permanent `🔧 **Tool #N**: \`Name\``
  row with full formatted input; nothing is ever merged or cleared. Worse: the
  `EventToolResult` case has no `streamCard` branch at all (unlike its sibling
  `EventThinking`/`EventToolUse`/`EventText` cases) — every tool result is sent as an
  independent, un-editable chat message via `e.sendRaw`, bypassing the card entirely.

DingTalk's AI Card is opt-in via `card_template_id` (currently unconfigured on the
user's instance) — until it's configured, DingTalk runs the card-less fallback path,
where every tool event is already its own message with no way to merge them (out of
scope here; see "Out of scope").

## Scope

- Only `EventToolUse` / `EventToolResult` rendering on the `StreamingCardPlatform`
  path (`cardToolEntry` / `buildCardContent`). Thinking text (`cardThinkingText`) is
  untouched — stays fully visible, stays in the finalized card.
- Because this path is shared, Slack inherits the same behavior. No platform-name
  special-casing.
- New independent config toggle (platform-disable-list), not a rewrite of the existing
  `tool_title` flag's semantics.
- No changes to Feishu's `ToolStep` machinery — DingTalk/Slack get a parallel
  implementation on `cardToolEntry`, since that's upstream-owned code and this fork
  keeps changes additive to ease rebases.

## Decisions

| # | Question | Decision |
|---|---|---|
| 1 | Align DingTalk fully with Feishu's tool-title behavior? | Yes — full behavioral parity (merge, title-only, transient clear/reappear, no trace on finalize). |
| 2 | Also transient-ize Thinking text (Feishu's implementation collapses both together as a side effect)? | No. Only tool calls. Thinking stays as-is (always visible, kept on finalize). |
| 3 | The shared `StreamingCardPlatform` path also covers Slack — allow the change to affect Slack too, or special-case DingTalk only? | Allow it. No platform-name branching exists elsewhere in this codebase for this kind of behavior; special-casing by name would be a one-off pattern and increase upstream-merge friction. Slack has the same spam problem today. |
| 4 | Feishu's actual code (not its `CONTEXT.md` prose) only clears the tool area once per turn, on the *first* non-empty body-text event — not repeatedly on every tool→text→tool→text cycle within one turn. Match the code, or the more thorough prose description? | Match the actual code (once per turn, first text event only). A repeating-clear fix, if wanted, is a separate decision affecting both platforms — not bundled into this alignment task. |
| 5 | `EventToolResult` currently skips `streamCard` entirely and sends an independent message every time. Fix this gap as part of this task? | Yes. Add a `streamCard != nil` branch mirroring its sibling cases; the result event is absorbed into the card (see #9), never sent standalone. |
| 6 | Render merged titles with DingTalk's raw tool name (`Bash ×3`) or Feishu's humanized name (`Run command ×3`, via `platform/feishu`'s private `toolDescriptors` table)? | Raw tool name. Humanization is a private, Feishu-only mapping table; porting/duplicating it is a separate feature, not part of aligning the merge/collapse *mechanism*. |
| 7 | Reuse the single project-wide `tool_title` toggle for DingTalk too, or add finer-grained control? | Add `tool_title_disabled_platforms` (list), mirroring the existing `[stream_preview] disabled_platforms` precedent. DingTalk's implementation is brand-new/untested; Feishu's is shipped and battle-tested. Coupling them to one switch means a DingTalk regression forces disabling Feishu too — against this fork's stated "independently toggleable" design principle (see `CONTEXT.md`). Default: empty list, no behavior change today. |
| 8 | Merged row format: keep the `Tool #N` sequence prefix (`Tool #3: Bash ×3`) or drop it? | Drop it — `🔧 \`Bash ×3\`` only. Once merged, "the Nth call" is no longer a meaningful number. |
| 9 | Should the absorbed `EventToolResult` add a completion/success marker to the row (e.g. a ✅)? | No — pure `continue`, zero visual change on result. Matches Feishu's `TitleOnly` rendering, which ignores `Done`/`Success`/`ExitCode` entirely. Avoids needing an aggregation rule for "3 calls merged, 2 succeeded" during a still-transient row. |
| 10 | Hide input detail only on rows that actually merged (≥2 calls), or unconditionally whenever the toggle is on (even for a single, non-repeated call)? | Unconditionally, whenever the toggle is on — matches Feishu's actual behavior (`TitleOnly` is set on every new row regardless of repeat count), not a narrower "only when spam actually happens" reading. |
| 11 | Prerequisite: DingTalk AI Card requires `card_template_id` (+ optional `card_template_key`, default `"content"`, must match the template's body variable name; + optional `card_throttle_ms`, default 300ms). Confirmed unset on the user's instance today. | User will add `card_template_id`/`card_template_key`/`card_throttle_ms` under `[[projects.platforms]] type = "dingtalk"` → `[projects.platforms.options]`, and verify post-deploy that the AI Card actually renders as one editable, in-place-updating message (not several independent messages) before judging whether this change resolved the spam. |
| 12 | Card-creation failure / API degrade-lockout still falls back to the pre-existing independent-message-per-event path (no card to merge into). Mitigate this edge case too? | No — left as-is. Low-frequency error state; a fix would need an entirely separate merge mechanism (no editable message to update), disproportionate to its likelihood once the card template is configured correctly. |

## Implementation Plan

1. **`core/engine.go`**
   - `cardToolEntry`: add `Count int`.
   - New helper (parallel to `appendToolStep`, but operating on `cardToolEntry`) that
     merges into the last entry when its `Name` matches the incoming tool name and the
     platform's tool-title toggle is on; otherwise appends a new entry with `Count: 1`.
   - `buildCardContent`: render a tool row as `🔧 \`Name\`` (Count 1) or `🔧 \`Name ×N\``
     (Count > 1) when the toggle is on for this platform — no `Tool #N` prefix, no
     input block. Preserve today's `🔧 **Tool #%d**: \`Name\`` + input rendering when the
     toggle is off (existing behavior, e.g. for platforms/configs that keep it disabled).
   - `EventToolResult` case: add a `streamCard != nil && !streamCard.Failed()` branch
     mirroring `EventToolUse`'s shape — `continue` without mutating `cardToolCalls` or
     re-rendering (per decision #9), so the result never reaches `e.sendRaw`.
   - `EventText` case (streamCard branch): once `cardAnswerText` receives its first
     content this turn (equivalent to the rich-card path's `partialText == ""` guard),
     clear `cardToolCalls` when the toggle is on for this platform — so a later
     tool→text cycle **within the same turn as already agreed in #4** does not re-clear,
     matching Feishu's actual (once-per-turn) behavior.
   - Turn-reset point (existing `cardToolCalls = nil` for the queued-message path)
     needs no change — already clears per new turn.
   - New small helper `e.toolTitleEnabledFor(p Platform) bool`: `e.toolTitleTransient &&
     platform name not in e.toolTitleDisabledPlatforms`.
   - New engine field `toolTitleDisabledPlatforms []string` + setter
     `SetToolTitleDisabledPlatforms([]string)`.

2. **`config/config.go`**
   - `DisplayConfig.ToolTitleDisabledPlatforms []string` (toml:
     `tool_title_disabled_platforms,omitempty`), same shape as
     `StreamPreviewConfig.DisabledPlatforms`.
   - New resolver `EffectiveToolTitleDisabledPlatforms(cfg, proj) []string` (project
     overrides global, same precedence pattern as the other `Effective*` helpers).

3. **`cmd/cc-connect/main.go`**
   - Both `engine.SetToolTitleTransient(...)` call sites get a companion
     `engine.SetToolTitleDisabledPlatforms(config.EffectiveToolTitleDisabledPlatforms(cfg, ...))`
     call.

4. **`config.example.toml`**
   - Document `tool_title_disabled_platforms` next to the existing `tool_title` comment.
   - Document DingTalk's `card_template_id` / `card_template_key` / `card_throttle_ms`
     (currently entirely undocumented — an existing gap, not a regression from this
     change).

5. **Tests**
   - `core/engine_test.go` (or a new `core/card_tool_entry_test.go`): merge-counting
     helper (consecutive same-name merges, different-name doesn't), title-only
     rendering format, once-per-turn clear-on-first-text, `EventToolResult` absorbed by
     `streamCard` (no standalone send, no card mutation).
   - `config/config_test.go`: `EffectiveToolTitleDisabledPlatforms` precedence
     (project overrides global; default empty).
   - Existing DingTalk/Slack streaming-card tests should continue to pass unmodified
     (toggle defaults to on, so verify old assertions are updated if they hard-coded the
     pre-merge format).

## Out of scope (raised and explicitly deferred, not forgotten)

- Card-creation-failure / degrade-lockout fallback path still spams one message per
  tool event (#12) — unaddressed.
- Repeating (not just once-per-turn) tool-area clear within a single turn — would need
  a matching change to Feishu's implementation too; not bundled here (#4).
- Humanized tool titles (`Run command` instead of `Bash`) for DingTalk/Slack — not
  bundled here (#6).
- Feishu file-vs-image attachment save-path question raised mid-session — unresolved,
  untriaged, unrelated to tool-title rendering; needs its own follow-up once the user
  clarifies the specific failure observed.
