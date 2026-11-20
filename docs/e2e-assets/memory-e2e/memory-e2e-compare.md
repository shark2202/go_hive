| check | result | detail |
| --- | --- | --- |
| timeline system prompt identical | PASS | 0f2ce524f25ca870 vs 0f2ce524f25ca870 |
| timeline system prompt carries the schema | PASS |  |
| crafted-session prompt block identical | PASS | go=True py=True |
| recall base system prompt identical | PASS | 5b6a389a3ce5b5a3 vs 5b6a389a3ce5b5a3 |
| recall user message format identical | PASS |  |
| recall manifest lines identical | PASS | 2 vs 2 lines |
| backfill timeline items semantically identical | PASS | 3 vs 3 items |
| timeline item schema (keys) identical | PASS | aliases,end,kind,start,text,turn |
| timeline_meta schema (keys) identical | PASS | model,updated_at,user_turns,version |
| timeline_meta version/user_turns agree | PASS | go={'model': 'ollama_chat/memory-e2e', 'updated_at': '2026-11-20T18:53:22', 'user_turns': 1, 'version': 1} py={'version': 1, 'user_turns': 1, 'model': 'openai/memory-e2e', 'updated_at': '2026-11-20T18:53:40'} |
| stop flush wrote the same item texts | PASS | 3 vs 3 items |
| TopK excerpt lines = 6 on both sides | PASS | go=6 py=6 |
| excerpt block introduction identical | PASS |  |

### Expected configuration differences

- recall selector wire prompt: Go appends the JSONMode hint (suffix present), Python sends response_format instead (absent)
- timeline meta model: go='ollama_chat/memory-e2e' py='openai/memory-e2e' (configured model names)
- timeline item JSON encoding: go=compact py=spaced (both `json.dumps`/`Encoder` defaults)
- recall injection timing: Go computes per turn synchronously; Python caches and refreshes in the background
