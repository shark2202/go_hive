#!/usr/bin/env bash
# check_parity.sh — parity 注解门禁
#
# 规则：go-hive 中每一个 func TestXxx 都必须在其正上方的注释块里带有
#   // Parity: <python 源路径>::<python 测试名>
# 或显式豁免：
#   // Parity-exempt: <原因>
#
# 允许注释块与 func 之间隔一个空行。非注释、非空行会清空缓存的注释块，
# 因此注解必须紧贴测试函数，不能漂移到别的测试上。
#
# 退出码：0 = 全部已标注；1 = 存在未标注测试；2 = 运行环境错误。
#
# 用法：bash go_hive/scripts/check_parity.sh [module_root]
# 默认 module_root 为本脚本所在目录的 ../go-hive。

set -u

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="${1:-${SCRIPT_DIR}/../go-hive}"

if [ ! -d "${ROOT}" ]; then
    echo "check_parity: module root not found: ${ROOT}" >&2
    exit 2
fi

ROOT="$(cd "${ROOT}" && pwd)"
cd "${ROOT}" || exit 2

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

missing_file="${TMP_DIR}/missing.txt"
: > "${missing_file}"

# 每个测试文件输出一行：<pkg> <annotated> <missing>
per_file="${TMP_DIR}/per_file.txt"
: > "${per_file}"

while IFS= read -r file; do
    pkg="$(dirname "${file}")"
    pkg="${pkg#./}"
    awk -v pkg="${pkg}" -v file="${file}" -v miss="${missing_file}" '
        function flush_test(line_no) {
            if (block ~ /Parity:/ || block ~ /Parity-exempt:/) {
                annotated++
            } else {
                missing++
                printf "%s:%d: %s\n", file, line_no, name >> miss
            }
            block = ""
            pending_blank = 0
        }
        /^[ \t]*\/\// {
            block = block $0 "\n"
            pending_blank = 0
            next
        }
        /^[ \t]*$/ {
            # 注释块之后的单个空行被允许，块内容保留
            if (block != "") pending_blank = 1
            next
        }
        /^func Test/ {
            # 只处理函数声明，探针行以 func Test 开头
            line = $0
            name = line
            sub(/^func /, "", name)
            sub(/\(.*/, "", name)
            flush_test(FNR)
            next
        }
        {
            # 任何其它代码行都切断注释块归属
            block = ""
            pending_blank = 0
        }
        END {
            printf "%s %d %d\n", pkg, annotated + 0, missing + 0
        }
    ' "${file}" >> "${per_file}"
done < <(find . -name '*_test.go' -type f | sort)

# 聚合到包
aggregate="${TMP_DIR}/aggregate.txt"
awk '{ a[$1] += $2; m[$1] += $3 } END { for (p in a) printf "%s %d %d\n", p, a[p], m[p] }' \
    "${per_file}" | sort > "${aggregate}"

total_t=0
total_a=0
total_m=0
{
    printf '%-40s %7s %10s %8s\n' "package" "tests" "annotated" "missing"
    printf '%-40s %7s %10s %8s\n' "----------------------------------------" "-------" "----------" "--------"
    while read -r pkg ann miss; do
        t=$((ann + miss))
        total_t=$((total_t + t))
        total_a=$((total_a + ann))
        total_m=$((total_m + miss))
        printf '%-40s %7d %10d %8d\n' "${pkg#"${ROOT}"/}" "${t}" "${ann}" "${miss}"
    done < "${aggregate}"
    printf '%-40s %7s %10s %8s\n' "----------------------------------------" "-------" "----------" "--------"
    printf '%-40s %7d %10d %8d\n' "TOTAL" "${total_t}" "${total_a}" "${total_m}"
} > "${TMP_DIR}/table.txt"

cat "${TMP_DIR}/table.txt"

if [ "${total_m}" -gt 0 ]; then
    echo
    echo "== 未标注的测试（缺少 // Parity: 或 // Parity-exempt:）=="
    cat "${missing_file}"
    echo
    echo "check_parity: FAIL — ${total_m}/${total_t} 个测试缺少 parity 注解或豁免说明。" >&2
    exit 1
fi

echo
echo "check_parity: OK — ${total_t} 个测试全部带有 parity 注解或豁免说明。"
exit 0
