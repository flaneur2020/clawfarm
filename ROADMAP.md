# ROADMAP — Clawfarm / Clawbox（RFC-004 / RFC-005 对齐版）

## 0) 基线与目标

本 roadmap 以以下 RFC 为准：

- `rfc/004-clawbox-mountable-single-file-format.md`
- `rfc/005-clawbox-mount-lifecycle-and-locking.md`

核心架构约束（已定版）：

1. `clawbox` 是单文件分发格式；
2. 运行时挂载到 `~/.clawfarm/claws/{CLAWID}/mount`（只读）；
3. `CLAWID` 来自 `name + inode hash`；
4. 并发互斥仅使用单一锁文件：`instance.flock`（`github.com/gofrs/flock`）；
5. `state.json` 仅用于状态展示，不参与占用判定；
6. 导出命令统一为 `export`（不再使用 `save` 命名）。

---

## 1) 当前进度快照（2026-02-10）

- ✅ **M1 已完成**：`internal/clawbox` 规格解析/校验（含 `CLAWID` 计算）
  - `internal/clawbox/spec.go`
  - `internal/clawbox/spec_test.go`
- ✅ **M2 已完成**：`internal/mount` 单锁模型已接入 `run/rm` 关键路径（含并发测试）
  - `internal/mount/manager.go`
  - `internal/mount/flock_locker.go`
  - `internal/mount/manager_test.go`
  - `internal/app/app.go`
  - `internal/app/app_test.go`
- ✅ **M3 已完成**：支持 `run <file.clawbox>` 与 `run .`（唯一文件自动发现）
- ✅ Go 版本基线已升级到 `go 1.24.x`
- 🟡 **M5 进行中**：`export/checkpoint/restore` 锁保护已落地，`export` 已支持默认脱敏扫描（可 `--allow-secrets` 旁路），下一步补齐真实 `.clawbox` 打包

---

## 2) 里程碑拆分（更新版）

## M2 — Mount lifecycle 接入 CLI（已完成）

**目标**：把 RFC-005 的单锁模型接入现有命令路径。

**交付**

- `run` 进入关键区前 `TryLock(instance.flock)`；
- `run` 在锁内完成挂载复用/冲突检查与 `state.json` 更新；
- `rm`（以及后续 stop 路径）在锁内释放挂载并更新状态；
- busy / mount conflict 以明确错误返回。

**验收**

- 并发 `run` 同一 `CLAWID`：一个成功，一个 `ErrBusy`；
- `state.json.active=true` 但锁可获取时，不会阻塞新 `run`；
- `go test ./...` 全绿。

---

## M3 — `run <file.clawbox>` 端到端（已完成）

**目标**：从 `.clawbox` 文件直接启动。

**交付**

- `run demo.clawbox --env .env`；
- `run .`（当前目录唯一 `.clawbox` 自动发现）；
- 读取 header → 校验 → 计算 `CLAWID`；
- 挂载路径固定：`~/.clawfarm/claws/{CLAWID}/mount`；
- 复用现有 VM 启动链路。

**验收**

- 给定合法 `.clawbox` 可启动；
- 多个 `.clawbox` 场景提示用户显式选择；
- `go test ./...` + 关键集成测试通过。

---

## M4 — OpenClaw preflight + TUI 引导

**目标**：启动前完成必填参数校验，失败要 fail-fast。

**交付**

- 从 `spec.openclaw.required_env` 读取必填项；
- 缺失时交互式 TUI 逐项输入（密钥掩码 `*`）；
- 非交互模式缺参直接失败；
- 合法性检查在建 VM 前完成。

**验收**

- 缺参时 VM 不会启动；
- 参数齐全时实例可启动并在 `ps` 中可见；
- 异常实例在 `ps` 可见且状态明确。

---

## M5 — `export` / `checkpoint` / `restore`（进行中）

**目标**：实现可导出、可回滚的运行闭环。

**交付**

- `export <CLAWID> <output.clawbox>`；
- `checkpoint <CLAWID> --name <name>`；
- `restore <CLAWID> <checkpoint>`；
- 三者均在锁保护下执行；
- `export` 支持脱敏扫描（默认阻断，可 `--allow-secrets` 旁路），后续可扩展 `--squash`。

**验收**

- 导出产物可再次 `run`；
- checkpoint/restore 可恢复运行层状态；
- 并发冲突场景行为一致且可预期。

---

## M6 — 可观测性（`ps` / `view`）

**目标**：用户能快速定位实例是否健康、是否卡住。

**交付**

- `ps` 展示状态（ready/unhealthy/exited）与最近错误；
- `view <CLAWID>` 展示实例、挂载、日志、端口与错误摘要；
- 状态来源统一（实例状态 + `state.json`）。

**验收**

- 不健康实例在 `ps` 可见且可定位原因；
- `view` 对存在/不存在实例行为清晰。

---

## M7 — 命令与目录收敛到 `clawfarm`

**目标**：从 `vclaw` 过渡到 `clawfarm` 语义。

**交付**

- 主命令切换/别名策略（`clawfarm` first-class）；
- 默认目录收敛：`~/.clawfarm`；
- 环境变量命名收敛（`CLAWFARM_*`）。

**验收**

- 新命令链路完整可用；
- 迁移策略文档化，行为可预测。

---

## M8 — GUI MVP（后续）

**目标**：GUI-first 的最小可用版本。

**交付**

- 实例列表、详情、日志；
- run/export 入口；
- `.env` 编辑与密文输入。

**验收**

- GUI 完成 run → view → export 基本闭环。

---

## 3) 横切任务

- 文档：README、命令帮助、FAQ 与 RFC 同步；
- 测试：
  - 单元：`clawbox` / `mount` / preflight / scan；
  - 集成：并发 run、异常恢复、export 回归；
- Makefile：补齐 `test`, `integration`, `build` 目标；
- 安全：密钥掩码、导出脱敏、错误信息不泄密。

---

## 4) 协作节奏（继续沿用）

1. 每次只做一个小里程碑（或半个里程碑）；
2. 实现 + 测试一起提交；
3. 跑 `go test ./...`；
4. 你验收后做 checkpoint commit。

---

## 5) 下一步建议（立即执行）

建议开始 **M5 第一段**：先落地 `export` 命令的最小实现（锁保护 + 基础导出路径 + 回归测试），再扩展到 `checkpoint/restore`。
