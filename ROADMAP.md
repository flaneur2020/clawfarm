# ROADMAP — Clawbox MVP Runtime First（以 RFC-004 / RFC-005 为准）

## 0) 约束基线

以以下 RFC 为最高优先级：

- `rfc/004-clawbox-mountable-single-file-format.md`
- `rfc/005-clawbox-mount-lifecycle-and-locking.md`

当前已确认的核心约束：

1. `clawbox` 是单文件分发格式；
2. 运行时挂载目标统一为：`.../{CLAWID}/mount`；
3. `CLAWID` 基于 `name + inode hash` 计算；
4. 并发互斥使用单一 `instance.flock`；
5. `state.json` 仅用于展示，不作为占用判定；
6. 命令名统一使用 `export`（不再混用 `save`）。

---

## 1) 优先级分层（按你最新要求）

## P0（必须先完成）

目标：**最小可用生产路径**，能稳定“拉起 OpenClaw VM + 基于 clawbox 运行”。

- `run <file.clawbox>`：支持 header-json clawbox；
- `run .`：当前目录唯一 `.clawbox` 自动发现；
- `run` 前置参数校验（含 OpenClaw 必填参数）；
- 缺参交互式输入（TUI 风格，密钥掩码 `*`）；
- `ps` 可见不健康实例（`unhealthy` + `last_error`）；
- 锁保护下的 `run/rm` 并发安全。

## P1（紧随其后）

目标：补齐 **clawbox 格式运行路径**，但继续保持实现简单。

- `.clawbox` 纯文本 JSON spec 模式（首字符 `{`）：
  - 不走 mount；
  - 直接下载 `base_image` / `layers`；
  - 做 SHA256 校验；
  - 执行 `provision`；
  - 复用本地缓存，已下载文件不重复下载。
- 与 mountable clawbox 路径并存，作为早期快速迭代通道；
- 持续补强集成测试覆盖（缓存复用、失败回滚、并发）。

## P2（降优先级）

- `export` 深化（脱敏策略、打包完善）；
- `checkpoint/restore` 能力完善；
- `rm`/回收策略增强；
- CLI 命名迁移到 `clawfarm` 与目录迁移到 `~/.clawfarm`；
- GUI first-class 与可视化运维能力。

---

## 2) 当前进度快照（2026-02-10）

## ✅ 已完成

- `internal/clawbox`：spec 解析/校验 + `CLAWID` 计算；
- `internal/mount`：基于 `instance.flock` 的单锁模型落地；
- `run/rm`：锁保护 + 并发冲突处理；
- `run <file.clawbox>` 与 `run .`；
- OpenClaw 参数预检：
  - 支持 `required_env`；
  - 缺参时交互式输入；
  - 非交互模式 fail-fast；
- `ps` 健康态展示（`ready/unhealthy/exited` + `last_error`）；
- **JSON-spec `.clawbox` 运行路径（P1 核心）**：
  - 检测规则：文件首个非空白字符为 `{`；
  - 下载 base/layer 到 `~/.vclaw/images/clawbox`（或 `VCLAW_CACHE_DIR`）；
  - SHA256 校验；
  - 缓存命中时不重复下载；
  - 执行 `provision` 命令；
  - 该模式下不设置 mount source。

## ✅ 测试状态

- `go test ./...` 全绿；
- 已有 JSON-spec 集成测试覆盖：
  - 首次下载并运行；
  - 二次运行缓存复用（无重复下载）；
  - SHA 不匹配时启动前失败。

---

## 3) 下一阶段建议（P0/P1 only）

1. 补齐“mountable clawbox 单文件”真实挂载链路（与 RFC-004 对齐）；
2. 统一 spec-json 与 mount 模式的 provision 执行语义；
3. 增加失败清理一致性（下载中断、provision 失败、启动失败）；
4. 增加端到端 smoke：`run -> ps -> rm`（两种 clawbox 模式都覆盖）。

---

## 4) 协作节奏

- 每次只推进一个可验收小切片；
- 代码 + 测试一起提交；
- 每个 checkpoint 都保持 `go test ./...` 通过；
- 若 RFC 与实现冲突，以 RFC-004/005 为准。
