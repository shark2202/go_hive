# emoh evidence commands

```bash
EMOH_GO_BIN=/Users/mac/.hermes/profiles/omh-tui/cache/scratch/emoh/bin/hive \
EMOH_PY_HIVE=/Users/mac/hive/.venv/bin/hive \
EMOH_PY_CORE=/Users/mac/hive/.wt/exports-memo/core \
EMOH_REPO_WRAPPER=/Users/mac/hive/hive \
EMOH_GO_MODULE=/Users/mac/hive/.wt/exports-memo/go_hive/go-hive \
EMOH_WORK=/Users/mac/.hermes/profiles/omh-tui/cache/scratch/emoh/work \
EMOH_OUT=/Users/mac/hive/.wt/exports-memo/go_hive/docs/e2e-assets/exports-baseline \
bash go_hive/scripts/emoh_exports_baseline.sh
```


### py/m1_cwd_repo — cwd=/Users/mac/.hermes/profiles/omh-tui/cache/scratch/emoh/work/py-m1_cwd_repo/repo, cwd has exports/: 1, launch=abs

```bash
HIVE_HOME=/Users/mac/.hermes/profiles/omh-tui/cache/scratch/emoh/work/py-m1_cwd_repo/home perl -e 'alarm shift; exec @ARGV' 90 /Users/mac/hive/.venv/bin/hive serve --port 1
```

### py/m2_cwd_home — cwd=/Users/mac/.hermes/profiles/omh-tui/cache/scratch/emoh/work/py-m2_cwd_home/homecwd, cwd has exports/: 1, launch=abs

```bash
HIVE_HOME=/Users/mac/.hermes/profiles/omh-tui/cache/scratch/emoh/work/py-m2_cwd_home/home perl -e 'alarm shift; exec @ARGV' 90 /Users/mac/hive/.venv/bin/hive serve --port 1
```

### py/m3_cwd_tmp — cwd=/Users/mac/.hermes/profiles/omh-tui/cache/scratch/emoh/work/py-m3_cwd_tmp/tmpcwd, cwd has exports/: 1, launch=abs

```bash
HIVE_HOME=/Users/mac/.hermes/profiles/omh-tui/cache/scratch/emoh/work/py-m3_cwd_tmp/home perl -e 'alarm shift; exec @ARGV' 90 /Users/mac/hive/.venv/bin/hive serve --port 1
```

### py/m4_cwd_neutral — cwd=/Users/mac/.hermes/profiles/omh-tui/cache/scratch/emoh/work/py-m4_cwd_neutral/neutral, cwd has exports/: 0, launch=abs

```bash
HIVE_HOME=/Users/mac/.hermes/profiles/omh-tui/cache/scratch/emoh/work/py-m4_cwd_neutral/home perl -e 'alarm shift; exec @ARGV' 90 /Users/mac/hive/.venv/bin/hive serve --port 1
```

### py/m4w_repo_wrapper — cwd=/Users/mac/.hermes/profiles/omh-tui/cache/scratch/emoh/work/py-m4w_repo_wrapper/neutral, cwd has exports/: 0, launch=wrapper

```bash
HIVE_HOME=/Users/mac/.hermes/profiles/omh-tui/cache/scratch/emoh/work/py-m4w_repo_wrapper/home perl -e 'alarm shift; exec @ARGV' 90 /Users/mac/hive/hive serve --port 1
```

### go/m1_cwd_repo — cwd=/Users/mac/.hermes/profiles/omh-tui/cache/scratch/emoh/work/go-m1_cwd_repo/repo, cwd has exports/: 1, launch=abs

```bash
HIVE_HOME=/Users/mac/.hermes/profiles/omh-tui/cache/scratch/emoh/work/go-m1_cwd_repo/home perl -e 'alarm shift; exec @ARGV' 90 /Users/mac/.hermes/profiles/omh-tui/cache/scratch/emoh/bin/hive serve --port 1
```

### go/m2_cwd_home — cwd=/Users/mac/.hermes/profiles/omh-tui/cache/scratch/emoh/work/go-m2_cwd_home/homecwd, cwd has exports/: 1, launch=abs

```bash
HIVE_HOME=/Users/mac/.hermes/profiles/omh-tui/cache/scratch/emoh/work/go-m2_cwd_home/home perl -e 'alarm shift; exec @ARGV' 90 /Users/mac/.hermes/profiles/omh-tui/cache/scratch/emoh/bin/hive serve --port 1
```

### go/m3_cwd_tmp — cwd=/Users/mac/.hermes/profiles/omh-tui/cache/scratch/emoh/work/go-m3_cwd_tmp/tmpcwd, cwd has exports/: 1, launch=abs

```bash
HIVE_HOME=/Users/mac/.hermes/profiles/omh-tui/cache/scratch/emoh/work/go-m3_cwd_tmp/home perl -e 'alarm shift; exec @ARGV' 90 /Users/mac/.hermes/profiles/omh-tui/cache/scratch/emoh/bin/hive serve --port 1
```

### go/m4_cwd_neutral — cwd=/Users/mac/.hermes/profiles/omh-tui/cache/scratch/emoh/work/go-m4_cwd_neutral/neutral, cwd has exports/: 0, launch=abs

```bash
HIVE_HOME=/Users/mac/.hermes/profiles/omh-tui/cache/scratch/emoh/work/go-m4_cwd_neutral/home perl -e 'alarm shift; exec @ARGV' 90 /Users/mac/.hermes/profiles/omh-tui/cache/scratch/emoh/bin/hive serve --port 1
```

### py api_explicit_abs

```bash
HIVE_HOME=/Users/mac/.hermes/profiles/omh-tui/cache/scratch/emoh/work/py-api-abs/home/python -c "run_migration(exports_dir=Path(/Users/mac/.hermes/profiles/omh-tui/cache/scratch/emoh/work/py-api-abs/abs_exports))"
```

### go api_explicit_abs

```bash
HIVE_HOME=/Users/mac/.hermes/profiles/omh-tui/cache/scratch/emoh/work/go-api-abs/home /Users/mac/.hermes/profiles/omh-tui/cache/scratch/emoh/work/emoh-go-probe abs /Users/mac/.hermes/profiles/omh-tui/cache/scratch/emoh/work/go-api-abs/home /Users/mac/.hermes/profiles/omh-tui/cache/scratch/emoh/work/go-api-abs/abs_exports
```

### go api_default_empty_cwd_relative

```bash
cd /Users/mac/.hermes/profiles/omh-tui/cache/scratch/emoh/work/go-api-default/cwd && HIVE_HOME=/Users/mac/.hermes/profiles/omh-tui/cache/scratch/emoh/work/go-api-default/home /Users/mac/.hermes/profiles/omh-tui/cache/scratch/emoh/work/emoh-go-probe default /Users/mac/.hermes/profiles/omh-tui/cache/scratch/emoh/work/go-api-default/home ""
```

### colony list fixture home: /Users/mac/.hermes/profiles/omh-tui/cache/scratch/emoh/work/list-home

```bash
HIVE_HOME=/Users/mac/.hermes/profiles/omh-tui/cache/scratch/emoh/work/list-home /Users/mac/hive/.venv/bin/hive colony list
HIVE_HOME=/Users/mac/.hermes/profiles/omh-tui/cache/scratch/emoh/work/list-home /Users/mac/hive/.venv/bin/hive colony list --json
HIVE_HOME=/Users/mac/.hermes/profiles/omh-tui/cache/scratch/emoh/work/list-home /Users/mac/.hermes/profiles/omh-tui/cache/scratch/emoh/bin/hive colony list
HIVE_HOME=/Users/mac/.hermes/profiles/omh-tui/cache/scratch/emoh/work/list-home /Users/mac/.hermes/profiles/omh-tui/cache/scratch/emoh/bin/hive colony list --json
```
