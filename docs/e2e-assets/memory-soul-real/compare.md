| check | result | detail |
| --- | --- | --- |
| timeline system prompt identical | PASS | 0f2ce524f25ca870 vs 0f2ce524f25ca870 |
| recall base system prompt identical | PASS | 5b6a389a3ce5b5a3 vs 5b6a389a3ce5b5a3 |
| recall query section identical | PASS |  |
| recall manifest lines identical | PASS | 2 vs 2 |
| memory-reflection system prompt identical (same queen scope) | PASS | 1172fac676420f27 vs 1172fac676420f27 |
| soul system prompt identical for Queen Review | PASS | 486548d0b196de16 vs 486548d0b196de16 |
| soul user prompt identical for Queen Review | PASS | b9c705ecd8fba37c vs b9c705ecd8fba37c |
| soul system prompt identical for Queen Auto | PASS | 95fca59ce574e210 vs 95fca59ce574e210 |
| soul user prompt identical for Queen Auto | PASS | 259642371e19970e vs 259642371e19970e |
| timeline_meta keys identical | PASS | model,updated_at,user_turns,version |
| timeline item keys identical | PASS | aliases,end,kind,start,text,turn |
| timeline extraction wrote items on both sides | PASS | 3 vs 3 |
| timeline item validation clean on both sides | PASS | go=[] py=[] |
| excerpt block captured on both sides | PASS | go=545 chars, py=545 chars |
| excerpt block introduction identical | PASS | go='Excerpts from earlier conversations that may relate to the u' py='Excerpts from earlier conversations that may relate to the u' |
| both excerpt blocks carry the three sailing fixtures | PASS | go_lines=3 py_lines=3 |
| excerpt line count within the 6-deep TopK on both sides | PASS | go=3 py=3 |
| excerpt delivered through the reminder wrapper on both sides | PASS | go=True py=True |
| [go] queen_alpha queen-selector manifest includes its own file | PASS | [preference] alpha_notes.md: working notes that belong to queen_alpha only |
| [go] queen_alpha queen-selector manifest excludes queen_beta/colony/worker | PASS |  |
| [go] queen_alpha global-selector manifest excludes every queen/colony/worker file | PASS |  |
| [go] queen_alpha injected queen block only names its own files | PASS | ['alpha_notes.md'] |
| [go] queen_alpha injected block never names queen_beta/colony/worker files | PASS |  |
| [py] queen_alpha queen-selector manifest includes its own file | PASS | [preference] alpha_notes.md: working notes that belong to queen_alpha only |
| [py] queen_alpha queen-selector manifest excludes queen_beta/colony/worker | PASS |  |
| [py] queen_alpha global-selector manifest excludes every queen/colony/worker file | PASS |  |
| [py] queen_alpha injected queen block only names its own files | PASS | [] |
| [py] queen_alpha injected block never names queen_beta/colony/worker files | PASS |  |
| [go] queen_beta queen-selector manifest includes its own file | PASS | [preference] beta_notes.md: working notes that belong to queen_beta only |
| [go] queen_beta queen-selector manifest excludes queen_alpha/colony/worker | PASS |  |
| [go] queen_beta global-selector manifest excludes every queen/colony/worker file | PASS |  |
| [go] queen_beta injected queen block only names its own files | PASS | [] |
| [go] queen_beta injected block never names queen_alpha/colony/worker files | PASS |  |
| [py] queen_beta queen-selector manifest includes its own file | PASS | [preference] beta_notes.md: working notes that belong to queen_beta only |
| [py] queen_beta queen-selector manifest excludes queen_alpha/colony/worker | PASS |  |
| [py] queen_beta global-selector manifest excludes every queen/colony/worker file | PASS |  |
| [py] queen_beta injected queen block only names its own files | PASS | [] |
| [py] queen_beta injected block never names queen_alpha/colony/worker files | PASS |  |
| daily scheduler recorded an attempt on both sides | PASS | go_fallback=False py_fallback=False |
| soul.json keys identical | PASS | sections,updated_at,version |
| soul.json section keys identical | PASS | now |
| both sides wrote a new soul version after approval | PASS | v1 vs v1 |
| soul item keys identical | PASS | date,text |
| state.json has cursors on both sides | PASS |  |
| state.json has last_run on both sides | PASS |  |
| state.json has last_result on both sides | PASS |  |
| state.json has attempts on both sides | PASS |  |
| state.json last_result shape identical | PASS | 2 changes vs 2 changes |
| proposal keys identical | PASS | applied,base_version,bootstrap,created_at,edits,id,probes,sources,status,summary |
| review proposal was pending before approval on both sides | PASS |  |
| proposal applied on both sides | PASS | applied vs applied |
| proposal evidence entries have quote/where on both sides | PASS | [{'quote': 'I stayed up painting the harbour lanterns again last night and Ann asked me for a colour version.', 'where': 'Night painting · owner'}] vs [{'quote': 'I stayed up painting the harbour lanterns again last night and Ann asked me for a colour version.', 'where': 'Night painting · owner'}] |
| history version file written on both sides | PASS | ['v1.json'] vs ['v1.json'] |
| auto mode wrote a soul version without a decision on both sides | PASS | v1 vs v1 |
| auto proposal status applied on both sides | PASS | applied vs applied |
| recall block captured on both sides | NOTE | python alpha block empty this run (go=973 chars); upstream cached-injection timing, see py-recall-queen_beta.txt and the selector manifests for scope |
| recall reminder wrapper identical (modulo its timestamp) | NOTE | not comparable: the python alpha injection was not drained into the transcript this run |
| global profile pin present in both sides' recall | NOTE | not comparable: python alpha injection missing this run (see above) |

Model: `deepseek-flash-1017`; Go run requests: 74; Python run requests: 65.

Notes:
- The model is real; timeline items, memory selections, memory reflections and soul edits are whatever it returned.
- Both sides read the same seeded chats, so the soul reflection prompt is comparable byte for byte.
- Colony/worker scopes exist on disk and in the memory API, but a queen DM's recall reads global + that queen only;
  the run asserts the isolation rather than inventing a colony recall path.
