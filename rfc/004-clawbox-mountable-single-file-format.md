# RFC 004 — Clawbox 可挂载单文件格式（Mountable Single-File）

- **Status:** Draft
- **Date:** 2026-02-10
- **Project:** `clawfarm`
- **Related:** `rfc/003-clawfarm-gui-first-and-clawbox-format.md`

## 1. 摘要

本 RFC 定义 `Clawbox` 的单文件可挂载格式：

- 一个 `.clawbox` 文件即可分发；
- 运行时将该文件挂载到 `~/.clawfarm/claws/{CLAWID}`；
- `CLAWID` 必须来自该 `.clawbox` 文件头部元信息（不是运行时随机生成）；
- 挂载内容只读，运行时变更通过 overlay（如 `run.qcow2`）记录。

该设计用于支撑稳定分享、可重复运行、以及可审计的实例来源追踪。

---

## 2. 背景与问题

当前 clawbox 设计偏向“目录结构 + 文件集合”，在分发和引用上存在问题：

1. 分享不方便：文件多、拷贝易遗漏；
2. 来源不稳定：运行时难以稳定定位唯一包标识；
3. 追踪困难：实例与其来源包关系不够强绑定。

我们需要一个“可直接挂载、单文件、带内置 ID”的格式。

---

## 3. 目标与非目标

## 3.1 Goals

- **G1:** `Clawbox` 成为单文件（`.clawbox`）可分发对象；
- **G2:** 运行时统一挂载到 `~/.clawfarm/claws/{CLAWID}`；
- **G3:** `CLAWID` 从文件元信息读取并作为实例来源标识；
- **G4:** 包体只读，运行变更不污染包体；
- **G5:** 校验（hash/完整性）在挂载前完成。

## 3.2 Non-goals

- 本 RFC 不定义 GUI 交互细节；
- 本 RFC 不定义 secrets 注入协议（仅约束不内嵌 secret）；
- 本 RFC 不限制具体 hypervisor（只定义包与挂载语义）。

---

## 4. 核心术语

- **Clawbox file:** 单个 `.clawbox` 文件。
- **CLAWID:** 包内声明的稳定标识符，用于挂载路径与实例来源关联。
- **Mount root:** `~/.clawfarm/claws/{CLAWID}`。
- **Runtime overlay:** 实例运行写层（如 `run.qcow2`）。

---

## 5. 文件格式提案（v1）

## 5.1 外层容器

`.clawbox` 是一个单文件容器，包含：

1. 固定头（magic + version + offsets）；
2. 元信息区（JSON header）；
3. 只读文件系统 payload（推荐 `squashfs`）。

## 5.2 Header（JSON）最小字段

```json
{
  "schema_version": 1,
  "claw_id": "clawbox-demo-20260210",
  "name": "demo-openclaw",
  "created_at_utc": "2026-02-10T00:00:00Z",
  "payload": {
    "fs_type": "squashfs",
    "offset": 4096,
    "size": 123456789,
    "sha256": "..."
  },
  "spec": {
    "base_image": {
      "ref": "ubuntu:24.04",
      "url": "https://...",
      "sha256": "..."
    },
    "openclaw": {
      "install_root": "/claw",
      "model_primary": "openai/gpt-5",
      "gateway_auth_mode": "token",
      "required_env": ["OPENAI_API_KEY", "OPENCLAW_GATEWAY_TOKEN"],
      "optional_env": ["DISCORD_TOKEN", "TELEGRAM_TOKEN"]
    }
  }
}
```

## 5.3 `claw_id` 约束

- 必填，且为包内权威 ID；
- 推荐正则：`^[a-z0-9][a-z0-9-]{2,63}$`；
- 同一 `claw_id` 对应不同 payload hash 时，默认拒绝覆盖挂载（需显式迁移命令）。

---

## 6. 挂载语义

## 6.1 挂载路径

运行 `clawfarm run demo.clawbox` 时：

1. 读取 header，拿到 `claw_id`；
2. 创建挂载点：`~/.clawfarm/claws/{CLAWID}`；
3. 挂载 `.clawbox` payload 到该路径（只读）；
4. 后续运行从该路径读取 `spec` 与 artifacts。

## 6.2 挂载前校验

挂载前必须完成：

- header schema version 校验；
- `claw_id` 合法性校验；
- payload `sha256` 校验；
- 可选签名校验（后续扩展）。

## 6.3 只读要求

- mount root 必须只读；
- 运行期写入（日志/状态/overlay）写到 `~/.clawfarm/instances/<instance-id>/`；
- `.env` 必须外置，不写入 mount root。

---

## 7. 运行时目录布局

```text
~/.clawfarm/
  claws/
    <CLAWID>/            # .clawbox 挂载点（只读）
  instances/
    <instance-id>/
      run.qcow2
      logs/
      meta.json
  env/
    <CLAWID>.env         # 可选默认 env 存放位置
  cache/
    mounts/
    blobs/
```

---

## 8. 命令行为变更

## 8.1 `clawfarm run <file.clawbox> --env <path>`

- 从 `.clawbox` 读取 `CLAWID`；
- 挂载到 `~/.clawfarm/claws/{CLAWID}`；
- 读取 `spec.openclaw.required_env` 并在启动前 preflight；
- 缺失必填项：
  - 交互模式：TUI 引导输入（secret 显示 `*`）；
  - 非交互模式：直接失败。

## 8.2 `clawfarm run .`

- 当前目录存在唯一 `.clawbox` 文件时可简写；
- 多个文件则报错并提示明确选择。

## 8.3 `clawfarm save <instance-id> --output xxx.clawbox`

- 导出为单文件 `.clawbox`；
- 默认生成新的 `claw_id`（避免与来源冲突）；
- 可选 `--preserve-clawid`（需要安全提示）。

---

## 9. 安全与合规

- `.clawbox` 不得包含明文 secrets；
- `save` 前进行脱敏扫描；
- 默认阻断疑似 secret 导出；
- mount root 只读，避免运行时污染分享包。

---

## 10. 兼容与迁移

## 10.1 与目录版 clawbox 的关系

- 目录版作为过渡格式；
- 提供转换命令：
  - `clawfarm pack <dir.clawbox> --output file.clawbox`
  - `clawfarm unpack file.clawbox --output dir.clawbox`

## 10.2 与旧 `vclaw` 兼容

- 运行核心（VM 启动链路）可复用；
- 新逻辑主要增加：文件解析、挂载、CLAWID 路径映射、preflight 绑定。

---

## 11. 实施里程碑

1. **M1:** `clawbox` header/spec 解析与校验；
2. **M2:** mount lifecycle（挂载/卸载/复用）；
3. **M3:** `run` 集成 `CLAWID` 路径与 preflight；
4. **M4:** `save` 导出单文件 + 扫描；
5. **M5:** `pack/unpack` 迁移工具。

---

## 12. 验收标准

满足以下即视为 RFC 落地：

1. `clawfarm run demo.clawbox` 可把包挂载到 `~/.clawfarm/claws/{CLAWID}`；
2. `CLAWID` 来自包内 header 字段且可审计；
3. 缺失必填 env 时，能在 VM 启动前完成引导或失败；
4. `save` 产出单文件 `.clawbox` 并默认执行脱敏策略。

---

## 13. 待决问题

1. v1 payload 文件系统最终选型：`squashfs`（推荐）还是 `erofs`；
2. macOS 挂载实现：FUSE 依赖策略（内置/可选）；
3. `claw_id` 冲突策略是否支持自动后缀迁移；
4. 是否在 v1 引入签名（`ed25519`）强校验。
