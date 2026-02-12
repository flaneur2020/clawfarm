# ROADMAP — Clawbox MVP Runtime First（以 RFC-007 为准）

## 0) 约束基线

以以下 RFC 为最高优先级：

- `rfc/007-current-design-consolidated.md`

当前已确认的核心约束：

1. `.clawbox` 是导入/导出的分发格式，不参与运行时挂载；
2. `run` 语义是 import-first，可从同一 `.clawbox` 启动多个实例；
3. 运行态主键统一为 `CLAWID`，支持 `run --name` 作为前缀；
4. 运行目录以 `~/.clawfarm` 为准，`blobs` 内容寻址为 `<sha256>`；
5. cloud-init 由 clawspec 生成，必须创建 `claw` 用户并配置 sudo `NOPASSWD:ALL`；
6. 命令名统一使用 `export`（不再混用 `save`）。

---

## 1) 优先级分层（按你最新要求）

## P0（必须先完成）

目标：**最小可用生产路径**，能稳定“拉起 OpenClaw VM + 基于 clawbox 运行”。

- `run <file.clawbox>`：支持 tar.gz `.clawbox`（含 `clawspec.json` + `run.qcow2`）导入并启动；
- `run --name`：支持实例可读前缀；
- `run` 前置参数校验（含 OpenClaw 必填参数）；
- 缺参交互式输入（TUI 风格，密钥掩码 `*`）；
- `ps` 可见不健康实例（`unhealthy` + `last_error`）；
- 锁保护下的 `run/rm` 并发安全（实例粒度）；
- cloud-init 由 clawspec 生成并包含 `claw` 用户、sudo NOPASSWD。

## P1（紧随其后）

目标：补齐 **clawbox 格式运行路径**，但继续保持实现简单。

- `run.qcow2` 不存在时的 base fallback 策略稳定化；
- `claw/` 挂载与 cloud-init provision 的端到端验收（真实 QEMU smoke）；
- 持续补强集成测试覆盖（导入校验、失败回滚、并发多实例）。

## P2（降优先级）

- `export` 深化（脱敏策略、打包完善）；
- `checkpoint/restore` 能力完善；
- `rm`/回收策略增强；
- CLI 体验持续收敛到 `clawfarm` 命令集合（含 `new/run/ps/stop`）；
- GUI first-class 与可视化运维能力。

---

## 2) 当前进度快照（2026-02-11）

## ✅ 已完成

- `internal/mount`：基于 `instance.flock` 的实例级互斥；
- `run/rm`：锁保护 + 并发冲突处理；
- `.clawbox` 纯文本 JSON spec 路径（首字符 `{`）可运行；
- `.clawbox` tar.gz v2 解析器已接入（`clawspec.json`）并开始导入链路；
- OpenClaw 参数预检：
  - 支持 `required_env`；
  - 缺参时交互式输入；
  - 非交互模式 fail-fast；
- `ps` 健康态展示（`ready/unhealthy/exited` + `last_error`）；
- **artifact 缓存**：
  - 检测规则：文件首个非空白字符为 `{`；
  - 下载 base/layer 到 `~/.clawfarm/blobs/<sha256>`；
  - 采用临时文件下载完成后 rename 到 `<sha256>`；
  - SHA256 校验；
  - 缓存命中时不重复下载；
  - JSON spec 模式执行 `provision` 命令；
  - JSON/tar 导入模式均不设置 mount source。

## ✅ 测试状态

- `go test ./...`（需 Go 1.24 工具链）；
- 已有 JSON-spec 测试覆盖：
  - 首次下载并运行；
  - 二次运行缓存复用（无重复下载）；
  - SHA 不匹配时启动前失败。
- 新增 tar v2 路径测试（进行中）：
  - `run <demo.clawbox> --name` 导入并启动；
  - 同包多实例；
  - 缺失 `clawspec.json` 失败。

---

## 3) 下一阶段建议（P0/P1 only）

1. 清理剩余 legacy 路径，保持 import-first runtime 模型唯一；
2. 完成 tar v2 导入路径收口（run.qcow2/base fallback、`claw/` 挂载）；
3. 增加失败清理一致性（下载中断、provision 失败、启动失败）；
4. 增加端到端 smoke：`run -> ps -> rm`（JSON 与 tar v2 都覆盖）。

---

## 4) 协作节奏

- 每次只推进一个可验收小切片；
- 代码 + 测试一起提交；
- 每个 checkpoint 都保持 `go test ./...` 通过；
- 若 RFC 与实现冲突，以 RFC-007 为准。
