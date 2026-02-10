# ROADMAP — Clawfarm / Clawbox（分阶段落地计划）

## 0) 目标

基于 `rfc/003-clawfarm-gui-first-and-clawbox-format.md`，按“可运行、可验证、可回退”的方式，把现有 `vclaw` 演进到 `clawfarm`：

- GUI-first（CLI 仍完整可用）
- `Clawbox` 可分享格式
- `new / run / ps / view / save` 闭环
- `.env` 外置、TUI 引导输入、启动前强校验
- `save` 脱敏扫描

---

## 1) 执行原则

- **小步快跑**：每个里程碑都可单独发布。
- **先可用再扩展**：先做目录版 `.clawbox`（`spec.json` + artifacts），后续再做单文件封装。
- **Fail-fast**：必填参数缺失/非法，必须在建 VM 前报错。
- **安全默认**：secrets 不写入 clawbox，交互输入默认掩码，导出默认脱敏阻断。
- **每步可验证**：每个里程碑必须包含测试与验收命令。

---

## 2) 里程碑拆分

## M0 — 稳定基线（1 次提交）

**目标**：在当前稳定代码上建立实施分支基线。

- 保持现有命令可用：`run/image/ps/suspend/resume/rm`
- 全量测试为绿
- 文档补齐：标注“RFC-003 roadmap 进行中”

**验收**

- `go test ./...` 全绿
- `make test` 全绿

---

## M1 — Clawbox 规格与 I/O（核心基础）

**目标**：实现 `spec.json` 的读写/校验能力。

**交付**

- 新增 `internal/clawbox`：
  - `Spec` 数据结构
  - `Load/Save/Validate`
  - schema version 检查
- 先支持“目录格式” clawbox（如 `demo.clawbox/`）

**验收**

- 单元测试覆盖：合法、缺字段、非法字段、版本不兼容
- 可以读写：`demo.clawbox/spec.json`

---

## M2 — `clawfarm new`（TUI 向导 + 模板生成）

**目标**：可创建可运行的 clawbox 草稿与 `.env` 模板。

**交付**

- 新命令：`clawfarm new`
- 支持参数：
  - `--output demo.clawbox`
  - `--env-output .env`
  - `--image-ref ubuntu:24.04`
  - `--model-primary openai/gpt-5`
  - `--gateway-auth-mode token|password|none`
  - `--with-discord/--with-telegram/--with-whatsapp`
- 若关键参数缺失，进入 TUI 引导
- 输出：
  - `demo.clawbox/spec.json`
  - `.env`（占位）

**验收**

- `clawfarm new` 在交互与非交互两种模式可工作
- 生成产物可通过 `clawbox.Validate`

---

## M3 — `clawfarm run <bundle> --env`（复用现有 VM 路径）

**目标**：支持从 clawbox 启动实例。

**交付**

- `clawfarm run demo.clawbox --env .env`
- 兼容：`clawfarm run .`（自动发现当前目录 clawbox）
- 解析 `spec.base_image.ref` → 复用现有 image manager 和 VM 启动链路
- 环境变量优先级：
  1. 显式 flags
  2. `--openclaw-env`
  3. `--env` / `--openclaw-env-file`
- 必填 env（来自 `spec.openclaw.required_env`）缺失时：
  - 交互模式：TUI 逐项输入（secret 显示 `*`）
  - 非交互模式：直接报错

**验收**

- 缺参时 VM 不会启动（fail-fast）
- 提供完整参数时实例可启动并在 `ps` 可见

---

## M4 — `clawfarm ps` / `clawfarm view`（可观测性）

**目标**：增强实例可视化信息。

**交付**

- `ps`：延续健康状态 + 错误摘要
- `view <clawid>`：实例详情（image、状态、端口、日志路径、错误）

**验收**

- `view` 对存在/不存在实例行为清晰
- 健康状态流转（ready/unhealthy/exited）持续可用

---

## M5 — `clawfarm save`（脱敏扫描 + 导出）

**目标**：从运行实例导出可分享包。

**交付**

- 命令：`clawfarm save <clawid> --output xxx.clawbox`
- 导出前扫描 secrets（关键词 + 正则）
- 默认策略：发现疑似 secret 则阻断导出
- 产物：
  - `spec.json`
  - `user.qcow2`（若可用）
  - 扫描结果摘要

**验收**

- 有风险时明确失败并给出路径
- 无风险时导出成功，可再次 `run`

---

## M6 — 存储布局与命名收敛（`.clawfarm`）

**目标**：目录与环境变量从 `vclaw` 迁移到 `clawfarm`。

**交付**

- 默认目录：`~/.clawfarm`
- 支持：`CLAWFARM_HOME / CLAWFARM_CACHE_DIR / CLAWFARM_DATA_DIR`
- （可选）兼容读取旧 `VCLAW_*`

**验收**

- 新目录布局生效
- 旧变量兼容策略文档化

---

## M7 — GUI MVP（第一版）

**目标**：实现最小可用 GUI。

**交付**

- 实例列表（状态、健康、端口）
- 实例详情（日志、错误）
- Run / Save 操作入口
- `.env` 可视编辑（密文输入）

**验收**

- GUI 可完成 run → view → save 基本闭环

---

## 3) 横切任务

- 文档：README、命令帮助、FAQ
- Makefile：新增 `build-clawfarm`、`integration-clawbox-*`
- 测试：
  - 单元：spec / env / preflight / scan
  - 集成：new → run → ps → save
- 兼容策略：`vclaw` 命令别名与迁移提示

---

## 4) 协作方式（我们一步一步来）

每一步都按这个节奏：

1. 你确认本步范围（1 个里程碑或其中一半）
2. 我实现 + 写测试
3. 我跑 `go test ./...`
4. 你验收后我提交 checkpoint commit

---

## 5) 下一步建议（马上开始）

建议先做 **M1（Clawbox 规格与 I/O）**，因为这是后续 `new/run/save` 的共同基础。

本步我会交付：

- `internal/clawbox/spec.go`
- `internal/clawbox/spec_test.go`
- `go test ./...` 全绿

如果你同意，我下一条就直接开始 M1。 
