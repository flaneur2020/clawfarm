# RFC 006 — Clawbox 作为导入/导出分发格式（非运行时挂载）

- **Status:** Draft
- **Date:** 2026-02-11
- **Project:** `clawfarm`
- **Supersedes (partial):**
  - `rfc/004-clawbox-mountable-single-file-format.md` 中“运行时 mount clawbox”语义
  - `rfc/005-clawbox-mount-lifecycle-and-locking.md` 中“按 clawbox/CLAWID 单实例互斥”语义

---

## 0. 基本思路（提案者意图）

这份 RFC 的核心不是“优化 mount”，而是**把 `.clawbox` 从运行时抽离出来，只保留分发职责**。

可以概括为 6 点：

1. `.clawbox` 只负责 import / export，不直接参与实时运行；
2. `clawfarm run xxx.clawbox` 的真实语义是 `import -> run`；
3. 不再要求“一个 clawbox 只能启动一个实例”，同包允许多实例并发；
4. `run` 增加 `--name`，让实例按人类可读名称组织和管理；
5. 包格式尽量简单：`.tar.gz` + `clawspec.json` + `run.qcow2`（可带 `claw/` 目录）；
6. 存储尽量简单：blob 统一进 `~/.clawfarm/blobs/<sha256>`，下载先临时文件，校验后重命名。

这个方向本质上是：

- **分发与运行解耦**（降低模型复杂度）；
- **运行期以实例为中心**（而不是以包为中心互斥）；
- **先做最小可用生产路径**（MVP 优先）。

---

## 1. 摘要

本 RFC 将 `.clawbox` 定义为**分发格式**，而不是运行时挂载格式：

1. `clawfarm run xxx.clawbox` 等价于“先 import 到 `~/.clawfarm`，再启动实例”；
2. 运行期不再挂载 `.clawbox`，`mount/` 目录从运行模型中移除；
3. 同一个 `.clawbox` 可以启动多个实例，不再限制“一包只能启动一个”；
4. `run` 增加 `--name`，用于实例命名（可读、可检索）；
5. `.clawbox` 采用 `.tar.gz` 容器，最小只包含：
   - `clawspec.json`
   - `run.qcow2`

此外，镜像结构简化为两层语义：

- 基础镜像（如 `ubuntu:22.04`）；
- 一个 overlay：`run.qcow2`（不再组织 layers 列表）。

---

## 2. 背景与问题

当前方案中，`.clawbox` 参与运行时 mount，并引入了以 `CLAWID` 为中心的“包级互斥”。这在实际使用中有几个问题：

1. **运行耦合过强**：分发文件直接参与运行态，恢复/并发语义复杂；
2. **并发受限**：同包单实例限制不利于快速复制多个实验环境；
3. **模型偏复杂**：`layers + mount + lock` 对 MVP 阶段成本偏高。

我们需要一个“分发与运行解耦”的最小可用模型。

---

## 3. 目标与非目标

## 3.1 Goals

- **G1:** `.clawbox` 仅用于导入/导出分发；
- **G2:** 运行时不依赖 mount `.clawbox`；
- **G3:** 同一个 `.clawbox` 可并发启动多个实例；
- **G4:** `run` 支持 `--name`；
- **G5:** 镜像结构简化为 `base image + run.qcow2`；
- **G6:** 全部运行态操作统一以 `CLAWID` 为主键。

## 3.2 Non-goals

- 本 RFC 不定义 GUI 交互细节；
- 不在本 RFC 引入多层镜像（layers）或复杂快照图；
- 不在本 RFC 强制引入签名系统（仅保留哈希校验）。

---

## 4. 术语（统一版）

- **CLAWID**：单次运行实例的唯一标识；所有运行态路径、锁、命令都以它为主键。
- **Claw Name**：`run --name` 提供的人类可读别名。
- **Source Clawbox**：输入的 `.clawbox` 分发文件。

约束：

- 本 RFC 不再使用 `PackageID` / `InstanceID` 术语；
- 同一个 `.clawbox` 多次运行会生成多个不同 `CLAWID`。

---

## 5. `.clawbox` 文件格式（v2）

`.clawbox` 是一个 `.tar.gz` 包，最小结构：

```text
<name>.clawbox   # 实际是 tar.gz
  clawspec.json
  run.qcow2
  claw/          # 运行时拷贝到 ~/.clawfarm/claws/<CLAWID>/claw/，并 mount 到 VM 内的 /claw 目录
    skills/
    SOUL.md
    MEMORY.md
```

`.clawbox` 文件中可能包含基础镜像，也可以不包含。如果不包含，则根据 `ref` 下载。

若包含 blob，校验后写入公共目录 `~/.clawfarm/blobs/<sha256>`：

1. 先写临时文件；
2. 校验 SHA256；
3. 原子重命名为 `<sha256>`。

`clawspec.json` 示例（简化）：

```json
{
  "schema_version": 2,
  "name": "demo-openclaw",
  "sha256": "xte334s",
  "image": [
    {
      "name": "base",
      "ref": "ubuntu:22.04",
      "sha256": "..."
    },
    {
      "name": "run",
      "ref": "clawbox:///run.qcow2",
      "sha256": "..."
    }
  ],
  "provision": [
    {
      "name": "shell",
      "shell": "bash",
      "script": "echo 'Hello, World!'"
    }
  ],
  "openclaw": {
    "model_primary": "openai/gpt-5",
    "gateway_auth_mode": "token",
    "required_env": ["OPENAI_API_KEY", "OPENCLAW_GATEWAY_TOKEN"],
    "optional_env": ["DISCORD_TOKEN"]
  }
}
```

约束：

- 不再出现 `layers` 字段；
- `run.qcow2` 必须与 spec 中对应条目的 `sha256` 一致；
- `.clawbox` 不应包含明文 secrets。
- `provision` 部分必须可转换为 cloud-init ISO 初始化数据。
- cloud-init 初始化过程中必须创建 `claw` 用户，且该用户具备 `sudo` 权限，且必须配置为 `NOPASSWD:ALL`。

---

## 6. 运行语义

## 6.1 `clawfarm run xxx.clawbox --name <claw-name>`

执行流程：

1. 解包到临时目录；
2. 校验 `clawspec.json` 与 `run.qcow2` 哈希；
3. 导入 blob 到 `~/.clawfarm/blobs`；
4. 生成新的 `CLAWID`；
5. 落盘实例目录 `~/.clawfarm/claws/<CLAWID>/`（含 `clawspec.json`、`run.qcow2`、`claw/`、`env`）；
6. 启动该 `CLAWID` 对应实例。

即：

- `import xxx.clawbox`
- `allocate CLAWID`
- `run <CLAWID>`

## 6.2 多实例语义

- 同一个 Source Clawbox 可并发运行多个实例；
- 每个实例都有独立 `CLAWID`；
- 互斥粒度是“实例级（CLAWID 级）”，不是“包级”。

## 6.3 镜像链路（简化）

仅保留两层：

- base image（如 `ubuntu:22.04`）；
- `run.qcow2`（导入后拷贝到 `~/.clawfarm/claws/<CLAWID>/run.qcow2`，运行写入直接落在该文件）。

## 6.4 cloud-init 生成规则

`clawspec` 在 `run` 期间会被渲染为 cloud-init（`user-data` + `meta-data`），并打包成 `init.iso`。

最小要求：

- 必须创建系统用户 `claw`；
- `claw` 必须具备 `sudo` 权限，且必须为 `NOPASSWD:ALL`；
- OpenClaw 安装与启动步骤由 `clawspec` 自动生成到 cloud-init 中，不依赖人工登录后手工执行。

---

## 7. 运行时目录布局（新）

```text
~/.clawfarm/
  blobs/
    <SHA256>                 # 内容寻址 blob（含导入的 base.qcow2、下载内容）

  claws/
    <CLAWID>/
      clawspec.json
      run.qcow2
      env
      init.iso               # 根据 clawspec 生成的 cloudinit 的 ISO
      claw/                  # 运行时映射到 vm 中的 /claw
```

说明：

- `mount/` 目录被移除；
- `.clawbox` 不参与运行时读写；
- 运行期只依赖本地导入产物 + base image。

---

## 8. 命令行为调整

## 8.1 `run`

- 新增：`--name <claw-name>`；
- 输入必须是 `.clawbox` 文件路径；
- 自动执行 import-first；
- 不再执行 clawbox 级别互斥。

## 8.2 `export`

```bash
clawfarm export <CLAWID> output.clawbox
```

- 导出为 tar.gz `.clawbox`；
- 包含 `clawspec.json` + `run.qcow2` + `claw/`（若存在）；
- 保持脱敏扫描策略。

---

## 9. 锁与并发模型

- 每个实例对应一个 `CLAWID`，锁文件对应 `~/.clawfarm/claws/<CLAWID>/lock`；
- `run/rm/checkpoint/restore/export` 只锁对应 `CLAWID`；
- 不存在“同一个 clawbox 全局锁”。

---

## 10. 迁移策略

不考虑向后兼容，按新模型重写即可。

1. 旧 mount 语义标记为 legacy；
2. 新 `run` 必须显式传 `.clawbox` 文件，不保留 `run .` 自动发现。

---

## 11. 验收标准

满足以下即通过：

1. 使用脚本可生成一个最小 `demo.clawbox`；
2. `clawfarm run demo.clawbox --name demo-a` 可启动，`clawfarm ps` 可见 active，`clawfarm logs demo-a` 可读日志，`clawfarm stop demo-a` 可关闭；
3. 同一 `demo.clawbox` 连续启动两次得到两个不同 `CLAWID`（`demo-a` / `demo-b`）且互不冲突；
4. 导入后内容落盘于 `~/.clawfarm`，blob 采用内容寻址。

---

## 12. 待决问题

1. `CLAWID` 生成策略：随机 ID 还是 `name + 随机后缀`；（随机ID）
2. `run.qcow2` 导出时是否默认做压缩/稀疏优化（倾向先简单）；
3. 是否需要单独 `import` 命令（当前先不做，`run` 隐式导入）。
