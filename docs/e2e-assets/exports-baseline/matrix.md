# emoh matrix — exports baseline per launch mode (measured)

Marker colonies: `probe_from_cwd` seeded at `<CWD>/exports/`, `probe_from_home` at `$HIVE_HOME/exports/`.
`migrated` = colony dirs (excluding `.migrations`) that appeared under `$HIVE_HOME/colonies` after the run.

| side | mode | CWD | CWD has exports/ | $HIVE_HOME has exports/ | launch | exit | .migrated-v2 | migrated | .migrations dir |
|---|---|---|---|---|---|---|---|---|---|
| py | m1_cwd_repo | repo | 1 | 1 | abs | 1 | yes | probe_from_cwd | yes |
| py | m2_cwd_home | homecwd | 1 | 1 | abs | 1 | yes | probe_from_cwd | yes |
| py | m3_cwd_tmp | tmpcwd | 1 | 1 | abs | 1 | yes | probe_from_cwd | yes |
| py | m4_cwd_neutral | neutral | 0 | 1 | abs | 1 | yes | - | no |
| py | m4w_repo_wrapper | neutral | 0 | 1 | wrapper | 1 | no | - | no |
| go | m1_cwd_repo | repo | 1 | 1 | abs | 1 | yes | probe_from_home | yes |
| go | m2_cwd_home | homecwd | 1 | 1 | abs | 1 | yes | probe_from_home | yes |
| go | m3_cwd_tmp | tmpcwd | 1 | 1 | abs | 1 | yes | probe_from_home | yes |
| go | m4_cwd_neutral | neutral | 0 | 1 | abs | 1 | yes | probe_from_home | yes |
| py | api_explicit_abs | (api) | explicit-abs | n/a | direct-call | 0 | yes | probe_from_abs | - |
| go | api_explicit_abs | (api) | explicit-abs | n/a | direct-call | 0 | yes | probe_from_abs | - |
| go | api_default_empty_cwd_relative | (api) | explicit-abs | n/a | direct-call | 0 | yes | probe_from_cwd | - |
