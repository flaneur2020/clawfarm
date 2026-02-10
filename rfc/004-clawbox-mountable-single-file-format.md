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
- **CLAWID:** (`包内声明的 name`, `文件的 inode 号码的哈希)`组成，用于挂载路径与实例来源关联，原则上一个 clawbox 文件只能打开一次。
- **Mount root:** `~/.clawfarm/claws/{CLAWID}/mount`。
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
    "layers": [
      {
        "ref": "xfce",
        "url": "https://...",
        "sha256": "..."
      }
    ],
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

---

## 6. 挂载语义

## 6.1 挂载路径

运行 `clawfarm run demo.clawbox` 时：

1. 读取 header，计算得到 `claw_id`；
2. 创建目录：`~/.clawfarm/claws/{CLAWID}`；
3. 挂载到目录：`~/.clawfarm/claws/{CLAWID}/mount`；
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
    <CLAWID>/            # .clawbox 目录
      mount/             # 挂载点（只读）
      run.qcow2          # 运行期写层（如 `run.qcow2`）
      state.json         # 运行状态记录
      env                # 环境变量文件
  blobs/
    <BLOBSHA256>        # blob 文件 (包括基础镜像的内容)
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

## 8.3 `clawfarm export <CLAWID> xxx.clawbox`

- 导出为单文件 `.clawbox`；
- 可选提供一个 `--name`;
- save 时，需要先 suspend 掉 claw 实例，save 后恢复执行。
- save 时，应当允许指定 `--squash` 将 run.qcow2 合并到 layers 中的 qcow2 文件，使 layers 只有一层。

## 8.4 `clawfarm checkpoint <CLAWID> --name <name>`

- checkpoint 执行期间，需要先 suspend 掉 claw 实例
- 记录 run.qcow2 的快照到 blob，记录 checkpoint 到 state.json。

## 8.5 `clawfarm restore <CLAWID> ['1 minute ago'| checkpointName]`

- 备份当前的 run.qcow2 到 checkpoint
- 恢复对应的 checkpoint 到 run.qcow2

---

## 9. 安全与合规

- `.clawbox` 不得包含明文 secrets；
- `save` 前进行脱敏扫描；
- 默认阻断疑似 secret 导出；
- mount root 只读，避免运行时污染分享包。

---

## 10. 兼容与迁移

不需要兼容 vclaw，可以从零实现，只要参考现有 vclaw 的代码仅作为参考即可。

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
2. macOS 挂载实现：FUSE 依赖策略（内置/可选）；(最好内置)
3. 是否在 v1 引入签名（`ed25519`）强校验。（最好有）
