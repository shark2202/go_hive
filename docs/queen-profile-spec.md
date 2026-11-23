# Queen profile spec — `queen_defaults/<queen_id>.yaml`

One YAML file per built-in queen. The **filename stem is the queen id** (`queen_brand_design`), and the file is the single source of truth for that queen's persona plus her capability defaults.

- Python reference: `core/framework/agents/queen/queen_defaults/*.yaml`, loaded by `framework/agents/queen/queen_profiles.py`.
- Go port: `go_hive/go-hive/internal/queen/queen_defaults/*.yaml`, embedded with `//go:embed queen_defaults/*.yaml` (`internal/queen/profiles.go`).
- Parity: all 13 files are **byte-identical** between the two trees (checked by `scripts/check_queen_profiles.py --check`).
- JSON Schema: `contracts/queen-profile.schema.json`.

## Fields

| Field | Type | Required | Meaning / use |
|---|---|---|---|
| `name` | text (non-empty) | ✅ | Display name; injected as `your name is {name}` in `<core_identity>` |
| `title` | text (non-empty) | ✅ | Role title (`Head of Brand & Design`); injected next to the name |
| `core_traits` | text | ✅ | One-paragraph character core; injected in `<core_identity>`; also the base of the queen-list summary |
| `skills` | text | ✅ | Prose skill description for prompt/API. **Not** the preset-skill list (that is `default_preset_skills`) |
| `hidden_background` | map[text]→text | ✅ | Behavioral engine rendered in `<hidden_background>`; never shown to the user |
| `hidden_background.past_wound` | text | ✅ | Formative failure that shapes judgment |
| `hidden_background.deep_motive` | text | ✅ | What she is actually optimizing for |
| `hidden_background.behavioral_mapping` | text | ✅ | How the motive shows up in observable behavior |
| `psychological_profile` | map[text]→text | ✅ | Rendered in `<psychological_profile>` |
| `psychological_profile.social_masks` | text | ✅ | Register per audience (founders / engineers / marketing) |
| `psychological_profile.anti_stereotype` | text | optional | Negative constraints; rewritten from third person to second person before rendering |
| `behavior_triggers` | list[{`trigger`,`reaction`}] | ✅ (≥1 item) | Rendered as `<behavior_rules>` interaction triggers; `reaction` also gets the pronoun rewrite |
| `world_lore` | map[text]→text | ✅ | Rendered in `<world_lore>` |
| `world_lore.habitat` | text | ✅ | Physical/sensory setting she inhabits |
| `world_lore.lexicon` | text | ✅ | Vocabulary she reaches for |
| `examples` | list[{`user`,`internal`,`response`}] | ✅ (≥1 item) | Roleplay few-shot. Shipped profiles carry 3–4; the runtime renders at most `max_examples` (colony/soul paths pass 0, authoring/eval pass all) |
| `portrait` | map[text]→text | ✅ | Avatar recipe for the UI (not prompt text) |
| `portrait.cut` | text | ✅ | Silhouette/crop key (`crop-thin`) |
| `portrait.face` | text | ✅ | Face shape |
| `portrait.hair` | text | ✅ | Hair (hex color or style key) |
| `portrait.skin` | text | ✅ | Skin tone (hex) |
| `portrait.beard` `brow` `collar` `eyes` `glasses` `mouth` `neck` `shirt` | text | optional | Optional layers; presence varies per queen |
| `default_tool_categories` | list[text] | ✅ (may be empty) | Capability key: MCP tool categories granted by default. Empty ⇒ every MCP tool allowed |
| `default_preset_skills` | list[text] | ✅ (may be empty) | Capability key: preset-scope skills enabled in memory at load |

Capability keys (`default_tool_categories`, `default_preset_skills`) are **stripped** before the profile is materialized to disk, so `profile.yaml` holds persona only.

## Validation

The YAML **file** has no runtime validator (it ships with the binary), but the API edit surface does. `PUT /api/queen/{queen_id}/persona` replaces the eight persona fields whole and enforces:

- text fields (`name`, `title`, `core_traits`, `skills`) must be strings; `name` and `title` cannot be blank;
- map fields (`hidden_background`, `psychological_profile`, `world_lore`) must be `map[str,str]` or `null`;
- `behavior_triggers` must be `list[{trigger: str, reaction: str}]` or `null`;
- any other key ⇒ `Unknown persona field '<key>'`.

Error strings are byte-identical between Python (`framework/server/routes_queens.py::_persona_problem`) and Go (`internal/server/routes_queens.go::queenPersonaProblem`). `portrait` is edited through the profile body, not the persona body; `examples` and the capability keys are not editable through this route.

## Lifecycle

1. On first use, `_materialize_default_queen` writes the **persona half** (`_persona_only`) to `$HIVE_HOME/agents/queens/<queen_id>/profile.yaml`. An existing `profile.yaml` is never overwritten (`EnsureDefaultQueens` is a no-op in Go for exactly this reason).
2. Session start renders the five-pillar identity prompt via `format_queen_identity_prompt` (Go: `queen.FormatQueenIdentityPrompt`, parity-tagged): `<core_identity>`, `<hidden_background>`, `<psychological_profile>`, `<behavior_rules>`, `<world_lore>`, then skills, then roleplay examples.
3. Dashboard edits rewrite `profile.yaml` with block-scalar formatting and keep up to 30 snapshots under `_profile_snapshots/`.
4. Titles can be superseded by product policy (`supersededTitles`, e.g. `queen_sales` ⇒ `Head of Sales`), independent of the file.

## Checking

```bash
.venv/bin/python go_hive/scripts/check_queen_profiles.py --check
```

Validates every `queen_defaults/*.yaml` in the Go tree against `contracts/queen-profile.schema.json`, byte-compares with the Python reference tree, and asserts both trees carry the same 13 queen ids. Exit code 0 = spec holds.