# judge-stall-real 实测汇总

- 生成：2026-11-21 16:09:42 CST
- judge：`http://127.0.0.1:18099` · {"status": "ok", "model": "0.8B-Instant-MLX-4bit", "kind": "instant", "images": true, "prompt_attention": "bidirectional", "engine": {"name": "mlx", "prompt_attention": "bidirectional", "images": true, "quantization": {"

| side | case | threshold | judge 请求 | p | 阈值比较 | 提醒进入转录 | 提醒到模型 | assistant/tool | 判定 |
| --- | --- | ---: | ---: | ---: | --- | --- | --- | --- | --- |
| py | stall-0.5 | 0.5 | 1 | 0.9365 | p≥阈值→提醒 | 是 | 是 | 3/0 | PASS |
| py | stall-0.7 | 0.7 | 1 | 0.9365 | p≥阈值→提醒 | 是 | 是 | 3/0 | PASS |
| py | borderline-0.5 | 0.5 | 1 | 0.6724 | p≥阈值→提醒 | 是 | 是 | 3/0 | PASS |
| py | borderline-0.7 | 0.7 | 1 | 0.6724 | p<阈值→不提醒 | 否 | 否 | 2/0 | PASS |
| py | tool-0.5 | 0.5 | 1 | 0.0209 | p<阈值→不提醒 | 否 | 否 | 3/1 | PASS |
| py | switch-off | 0.5 | 0 | — | — | 否 | 否 | 2/0 | PASS |
| py | judge-dead | 0.5 | 0 | — | — | 否 | 否 | 2/0 | PASS |
| py | no-user | 0.5 | 0 | — | — | 否 | 否 | 2/0 | PASS |
| go | stall-0.5 | 0.5 | 1 | 0.9365 | p≥阈值→提醒 | 是 | 是 | 3/0 | PASS |
| go | stall-0.7 | 0.7 | 1 | 0.9365 | p≥阈值→提醒 | 是 | 是 | 3/0 | PASS |
| go | borderline-0.5 | 0.5 | 1 | 0.6724 | p≥阈值→提醒 | 是 | 是 | 3/0 | PASS |
| go | borderline-0.7 | 0.7 | 1 | 0.6724 | p<阈值→不提醒 | 否 | 否 | 2/0 | PASS |
| go | tool-0.5 | 0.5 | 1 | 0.0209 | p<阈值→不提醒 | 否 | 否 | 3/1 | PASS |
| go | switch-off | 0.5 | 0 | — | — | 否 | 否 | 2/0 | PASS |
| go | judge-dead | 0.5 | 0 | — | — | 否 | 否 | 2/0 | PASS |
| go | no-user | 0.5 | 0 | — | — | 否 | 否 | 2/0 | PASS |

## judge 请求体两侧比对

- `borderline-0.5`: 请求体一致；p(py)=0.672394 p(go)=0.672394
- `borderline-0.7`: 请求体一致；p(py)=0.672394 p(go)=0.672394
- `stall-0.5`: 请求体一致；p(py)=0.936458 p(go)=0.936458
- `stall-0.7`: 请求体一致；p(py)=0.936458 p(go)=0.936458
- `tool-0.5`: 请求体一致；p(py)=0.020912 p(go)=0.020912

## 同一请求 3 次 p 稳定性

- `py-stall-0.5`: 0.936458, 0.936458, 0.936458
- `py-borderline-0.5`: 0.672394, 0.672394, 0.672394
- `go-stall-0.5`: 0.936458, 0.936458, 0.936458
- `go-borderline-0.5`: 0.672394, 0.672394, 0.672394
