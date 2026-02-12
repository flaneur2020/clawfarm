# RFC 003 — Clawfarm GUI-First 产品定位与 Clawbox 打包格式

- **Status:** Draft
- **Date:** 2026-02-10
- **Project:** `clawfarm`（`vclaw` 的下一阶段演进提案）
- **Related:** `rfc/001-initial-design.md`, `rfc/002-openclaw-full-runtime-requirements.md`

## 1. 摘要

本 RFC 提议将现有 VM 运行器演进为 **GUI-first 的 OpenClaw 运行与分享平台**：

1. 以 GUI 为一等公民（CLI 作为自动化和高级入口）；
2. 引入可分享的 `Clawbox` 打包格式；
3. 以可堆叠块设备（QEMU backing file）实现“不可变基础镜像 + 可变运行层”；
4. 在 `run/new` 阶段通过 TUI 引导必填密钥输入，并把敏感信息收敛到外部 `.env`；
5. 支持从运行实例 `save` 为可分享包，包含脱敏扫描。

## 2. 产品定位

### 2.1 GUI first citizen

- 现有传统 VM 管理工具多为 CLI-centric，对 OpenClaw 的目标用户门槛偏高。
- `clawfarm` 目标是让“运行、查看、保存、分享 OpenClaw 环境”可视化、低门槛。
- CLI 保留完整能力，用于自动化、CI、远程批处理。

### 2.2 社区维护装机脚本

- provision 脚本由社区维护，支持版本化与来源校验。
- 脚本可随 `Clawbox` 声明（spec）分发，做到可复现安装流程。

### 2.3 可分享格式

- 支持把 OpenClaw 运行环境打包成 `*.clawbox`，供团队分发与复用。
- 机密不内嵌入包体，统一通过外部 `.env` 注入。

## 3. 目标与非目标

### 3.1 Goals

- **G1:** 提供 GUI-first 交互体验与状态可视化（实例、日志、集成状态）。
- **G2:** 定义并实现 `Clawbox` 共享格式。
- **G3:** 保持基础镜像不可变，所有修改落在可变 overlay 层。
- **G4:** 在 `new/run` 中通过 TUI 对 API key/集成 token 做强引导与校验。
- **G5:** `save` 输出时执行脱敏扫描，降低泄漏风险。

### 3.2 Non-goals (本期)

- 不做多节点调度或集群编排。
- 不做跨 hypervisor 统一层（先聚焦 QEMU 路径）。
- 不在 `Clawbox` 中存放明文 secrets。

## 4. 术语

- **Clawbox:** 可分享的 OpenClaw 运行包，后缀建议 `.clawbox`。
- **Base image:** 不可变基础 OS 镜像（qcow2 或 raw，经校验后缓存）。
- **Layer:** 叠加块层（qcow2 backing chain 中间层）。
- **run.qcow2:** 实例运行期最顶层可写层。
- **Env FS:** 由 `.env` 生成并单独挂载的只读配置文件系统。

## 5. 命令设计（CLI + TUI）

## 5.1 `clawfarm new`

用途：创建 `.clawbox` 与 `.env`（或 `.env.example`），并通过 TUI 引导。

示例：

```bash
clawfarm new
```

流程（TUI）：

1. 选择基础 OS 镜像来源与版本；
2. 输入 OpenClaw 主模型（provider/model）；
3. 选择聊天集成（Discord/Telegram/WhatsApp）；
4. 生成 `spec`、provision 脚本引用、`.env` 模板；
5. 输出 `xxx.clawbox`。

## 5.2 `clawfarm run xxx.clawbox --env .env`

示例：

```bash
clawfarm run demo.clawbox --env .env
```

若未提供 `--env`：

- 自动进入 TUI 引导，逐项要求 API key / token；
- 校验不通过则 **在建 VM 前直接报错**（fail-fast）；
- 校验通过后生成到  `~/.clawbox/claws/{CLAWID}/env` ，再启动 VM。

也支持简写：

```bash
clawfarm run .
```

（当当前目录存在默认 `clawbox`/工程描述时）。

## 5.3 `clawfarm ps`

- 显示实例列表、状态、健康度、错误摘要。

## 5.4 `clawfarm view <clawid>`

- 打开 GUI 详情页（日志、资源、集成状态、环境诊断）。

## 5.5 `clawfarm save <clawid> --output xxx.clawbox`

- 从运行实例导出可分享包；
- 导出前执行脱敏扫描；
- 扫描失败默认阻断导出（可评估后续加入 `--allow-secrets` 逃生阀）。

## 6. Clawbox 格式提案

## 6.1 形态

`Clawbox` 作为单文件分发格式，内部是结构化文件系统。

需要考虑使用怎样的 文件格式，使之易于 mount。

每个 clawbox 文件，在启动后，会 mount 到 ~/.clawfarm/claws/{CLAWID}/mount/

建议结构：

```text
bundle.clawbox/
  spec.json
  blobs/
    x8923/
    d9sad/
  user.qcow2
```

> 注：目录名示例 `x8923/`, `d9sad/` 来自提案，可进一步规范为 digest 路径（如 `sha256/<digest>`）。

## 6.2 `spec.json` 核心字段

- 基础 OS 镜像地址与 `sha256`；
- qcow2 layer 的 `sha256` 列表，或外部下载地址（例如 R2）；
- provision 脚本（内嵌或引用 + 校验）；
- OpenClaw 安装目标目录（固定 `/claw`）；
- 所需环境变量声明（required/optional，含 chat integrations）。

示例（草案）：

```json
{
  "schema_version": 1,
  "name": "demo-openclaw",
  "base_image": {
    "url": "https://.../ubuntu-24.04.qcow2",
    "sha256": "..."
  },
  "layers": [
    {"sha256": "...", "url": "https://r2.../layer1.qcow2"},
    {"sha256": "...", "url": "https://r2.../layer2.qcow2"}
  ],
  "provision": {
    "entrypoint": "scripts/provision.sh",
    "sha256": "..."
  },
  "openclaw": {
    "install_root": "/claw",
    "required_env": ["OPENAI_API_KEY"],
    "optional_env": ["DISCORD_TOKEN", "TELEGRAM_TOKEN"]
  }
}
```

## 6.3 外部 `.env` 约束

- `.env` 必须外置，不打进 `.clawbox`；
- 默认挂载为只读；
- 可由 `new/run` 的 TUI 自动生成。

## 7. 块设备与运行时设计

## 7.1 不可变基础 + 可变 overlay

基于 QEMU backing file 机制：

```text
base.qcow2 (immutable)
  <- layer1.qcow2 (immutable)
    <- layer2.qcow2 (immutable)
      <- run.qcow2 (mutable, per instance)
```

- 基础 VM 保持不可变；
- 用户修改只写入 `run.qcow2`；
- `save` 时将可分享的变更层导出为新 `user.qcow2` / layer。

## 7.2 `.env` 挂载

- `.env` 不进入 backing chain；
- 通过独立 fs mount 注入（例如 virtiofs/9p 的只读挂载）；
- Guest 内统一映射到 `/claw/.env` 或 `/claw/env/.env`。

## 7.3 OpenClaw 目录约定

- 与 OpenClaw 相关的安装/运行内容统一放在 `/claw`；
- 便于扫描、导出与诊断。

## 8. 本地目录布局

建议：

```text
~/.clawfarm/
  blobs/
    sx82xa/
    xu8104/
  instances/
    <clawid>/
      run.qcow2
      logs/
      meta.json
```

- `blobs/`：基础镜像、层镜像、导出镜像缓存；
- `instances/<clawid>/run.qcow2`：该实例运行期可写层。

## 9. TUI 引导与校验规则

## 9.1 引导触发

在以下场景触发 TUI：

- `run` 缺少 `spec.required_env` 中任意必填项；
- 配置了 chat integration 但 token 缺失；
- provider 与 key 不匹配（如 `openai/...` 但无 `OPENAI_API_KEY`）。

## 9.2 校验原则

- **本地格式校验**：空值、基础格式、provider/model 结构；
- **一致性校验**：integration 声明与 token 完整性；
- **预检失败即终止**：在创建 VM 前报错。

## 10. `save` 脱敏扫描策略

导出前执行扫描：

1. 检查常见密钥模式（API key/token/password/secret）；
2. 扫描高风险路径（shell history、临时日志、配置快照）；
3. 生成扫描报告；
4. 默认阻断含明文 secret 的导出。

## 11. GUI 范围（第一阶段）

- 实例卡片（状态、资源、端口、健康）；
- 日志查看（OpenClaw + VM）；
- `.env` 字段编辑（密文输入）；
- 一键导入/运行 `.clawbox`；
- 一键导出（附扫描报告）。

## 12. 里程碑

1. **M1:** `Clawbox spec v1` + 基础读写实现。
2. **M2:** `new/run/save/ps/view` CLI 打通。
3. **M3:** backing chain + `run.qcow2` 生命周期管理。
4. **M4:** TUI 引导与 preflight 失败前置。
5. **M5:** 脱敏扫描与导出阻断策略。
6. **M6:** GUI MVP（运行、查看、导入导出）。

## 13. 风险与缓解

- **R1: 格式复杂度增长** → 先冻结 `spec v1` 最小字段集。
- **R2: 远端 layer 可用性（R2 链接失效）** → 支持本地镜像回填与校验失败提示。
- **R3: Secrets 泄漏风险** → 强制外部 `.env` + save 扫描 + GUI 密文输入。
- **R4: 层链性能问题** → 定期 flatten/compaction 工具。

## 14. 待决问题

1. `Clawbox` 单文件容器选择：`tar+zstd` 还是 `zip`？
2. `save` 是否允许“带风险继续导出”（`--allow-secrets`）？
3. GUI 与 CLI 的默认入口与分发策略如何统一？
4. `view` 是否仅 GUI，还是也输出 TUI 详情页？

## 15. 验收标准

满足以下条件视为 RFC 落地：

1. 可通过 `clawfarm new` 生成可运行的 `.clawbox` 与 `.env`；
2. `clawfarm run` 在缺少必填参数时可引导输入，并在非法时 VM 启动前失败；
3. `clawfarm save` 可输出包并给出脱敏扫描结论；
4. GUI 可完成基础的 run/ps/view/save 操作闭环。
