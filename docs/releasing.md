# 发布 GitHub Release

本流程供 MiCat-S/mosdns-x 的发布人在可信本地构建机执行。它会生成包含管理 UI 的跨平台包、校验内容与 SHA-256，最后创建公开 GitHub Release。生产服务器只下载这些资产，不参与构建。

## 准备构建机

需要 Bash、Python 3、Git、GitHub CLI、Go 1.26.3 或更新版本，以及 Node.js 22.22.2 或同一 22.x 主版本的更新版本。npm 依赖由 `web/package-lock.json` 锁定。先确认 `gh auth status` 显示的是有权发布 `MiCat-S/mosdns-x` 的账户。

切换到干净的 `main`，并确认本地 HEAD 与 `origin/main` 完全一致：

```bash
set -euo pipefail
git switch main
git fetch origin
test -z "$(git status --porcelain)"
test "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)"
git show --no-patch --format='构建提交 %H%n提交时间 %cI%n主题 %s' HEAD
go version
node --version
npm --version
gh auth status
```

选择明确的版本标签、标题和发布说明文件；不要从本文推断或自动递增当前版本：

```bash
RELEASE_TAG='vYY.MM.DD' # 同日追加发布使用 vYY.MM.DD.N
RELEASE_TITLE='<TITLE>'
RELEASE_NOTES_FILE='/absolute/path/to/release-notes.md'
test -s "$RELEASE_NOTES_FILE"
[[ "$RELEASE_TAG" =~ ^v[0-9]{2}\.[0-9]{2}\.[0-9]{2}(\.[1-9][0-9]*)?$ ]]
```

`release.py` 的 `--version` 是必填参数。当天首次发布使用 `YY.MM.DD`；同一天再次发布依次使用 `YY.MM.DD.1`、`YY.MM.DD.2`，修订号必须是没有前导零的正整数。两种格式都可以带 `v` 前缀。脚本会把规范化后的版本写入二进制和每个包内的 `BUILD-INFO.txt`，因此构建参数必须来自本次 `RELEASE_TAG`。

## 生成并检查资产

从仓库根目录运行本地发布脚本。`release/` 必须尚不存在，避免旧资产混入本次发布：

```bash
test ! -e release
RELEASE_COMMIT=$(git rev-parse HEAD)
python3 release.py --version "${RELEASE_TAG#v}"
```

脚本应产生以下 13 个 zip：

- `mosdns-darwin-amd64.zip`
- `mosdns-darwin-arm64.zip`
- `mosdns-linux-amd64.zip`
- `mosdns-linux-amd64-v3.zip`
- `mosdns-linux-arm-5.zip`
- `mosdns-linux-arm-6.zip`
- `mosdns-linux-arm-7.zip`
- `mosdns-linux-arm64.zip`
- `mosdns-linux-mipsle-softfloat.zip`
- `mosdns-linux-mips64le-hardfloat.zip`
- `mosdns-linux-ppc64le.zip`
- `mosdns-freebsd-amd64.zip`
- `mosdns-windows-amd64.zip`

用固定清单检查没有缺包或多包，并逐个验证 zip、文件清单和构建元数据：

```bash
cat > /tmp/mosdns-expected-assets <<'EOF'
mosdns-darwin-amd64.zip
mosdns-darwin-arm64.zip
mosdns-freebsd-amd64.zip
mosdns-linux-amd64-v3.zip
mosdns-linux-amd64.zip
mosdns-linux-arm-5.zip
mosdns-linux-arm-6.zip
mosdns-linux-arm-7.zip
mosdns-linux-arm64.zip
mosdns-linux-mips64le-hardfloat.zip
mosdns-linux-mipsle-softfloat.zip
mosdns-linux-ppc64le.zip
mosdns-windows-amd64.zip
EOF
find release -maxdepth 1 -type f -name 'mosdns-*.zip' -exec basename {} \; \
  | LC_ALL=C sort > /tmp/mosdns-actual-assets
diff -u /tmp/mosdns-expected-assets /tmp/mosdns-actual-assets
while IFS= read -r asset; do
  unzip -tq "release/$asset"
  binary=mosdns
  case "$asset" in
    *-windows-*) binary=mosdns.exe ;;
  esac
  cat > /tmp/mosdns-expected-members <<EOF
$binary
README.md
config.yaml
LICENSE
examples/control-production-proxy.yaml
examples/control-production-mysql.yaml
examples/Caddyfile
examples/mosdns.service
BUILD-INFO.txt
EOF
  unzip -Z1 "release/$asset" | LC_ALL=C sort > /tmp/mosdns-actual-members
  LC_ALL=C sort /tmp/mosdns-expected-members -o /tmp/mosdns-expected-members
  diff -u /tmp/mosdns-expected-members /tmp/mosdns-actual-members
  test "$(unzip -p "release/$asset" BUILD-INFO.txt | sed -n 's/^version=//p')" = "$RELEASE_TAG"
  test "$(unzip -p "release/$asset" BUILD-INFO.txt | sed -n 's/^commit=//p')" = "$RELEASE_COMMIT"
done < /tmp/mosdns-expected-assets
rm -f /tmp/mosdns-expected-assets /tmp/mosdns-actual-assets \
  /tmp/mosdns-expected-members /tmp/mosdns-actual-members
```

Windows 包的程序名为 `mosdns.exe`，其他包为 `mosdns`。每个包还包含 `README.md`、`config.yaml`、`LICENSE`、`BUILD-INFO.txt` 和四个生产部署示例。检查生成的 `config.yaml` 不含密码、token 或环境秘密。管理员密码不属于发布配置：部署时只通过 `control init-admin` 的 stdin 输入一次，数据库只保存随机盐和 Argon2id 摘要。

脚本会生成一个覆盖全部且仅覆盖这 13 个 zip 的校验文件。核对文件名清单并立即复验，不要在检查阶段重新生成或覆盖它：

```bash
awk '{ name=$2; sub(/^\*/, "", name); print name }' release/SHA256SUMS \
  | LC_ALL=C sort > /tmp/mosdns-checksum-assets
find release -maxdepth 1 -type f -name 'mosdns-*.zip' -exec basename {} \; \
  | LC_ALL=C sort > /tmp/mosdns-actual-assets
diff -u /tmp/mosdns-actual-assets /tmp/mosdns-checksum-assets
test "$(wc -l < release/SHA256SUMS)" -eq 13
(cd release && sha256sum -c SHA256SUMS)
rm -f /tmp/mosdns-checksum-assets /tmp/mosdns-actual-assets
```

发布前还应抽查至少一个本机构架包能够启动并显示预期版本，确认压缩包中的 UI、README、LICENSE 和配置均来自本次干净提交。跨平台二进制不能在本机直接运行时，至少保留构建矩阵成功日志和 zip 完整性结果。

## 创建 Release

下面的命令会创建或发布 Git tag 和公开 GitHub Release，并上传所有资产。这是对外发布步骤；只在版本、提交、说明、矩阵、包内容和校验和均已审核后执行：

```bash
gh release create "$RELEASE_TAG" \
  --repo MiCat-S/mosdns-x \
  --target "$RELEASE_COMMIT" \
  --title "$RELEASE_TITLE" \
  --notes-file "$RELEASE_NOTES_FILE" \
  --prerelease \
  release/mosdns-*.zip release/SHA256SUMS
```

首个多用户版本先按 prerelease 发布；完成生产验收后，可在 GitHub 上提升为正式 Release。只读的 `Verify release assets` 工作流监听 `release.published`，用于再次检查 13 个包和校验文件。若平台没有自动产生任务或需要重新运行，可执行 `gh workflow run release.yml --repo MiCat-S/mosdns-x -f tag="$RELEASE_TAG"`；该工作流只下载和验证已有资产，不在 GitHub 上构建。

完成后打开该 Release，确认 tag 指向本次记录的 `origin/main` 提交、13 个 zip 和 `SHA256SUMS` 均可下载。再从一个空临时目录按[部署教程](deployment.md)的单文件校验方式下载一个资产，确认公开下载的校验结果为 `OK`。
