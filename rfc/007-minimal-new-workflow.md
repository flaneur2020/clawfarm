# RFC 007: Minimal `clawfarm new` Workflow (Phase-1 VM Product)

- Status: Draft
- Author: clawfarm
- Last Updated: 2026-02-13

## 1) Key Ideas (Summary First)

本 RFC 定义一个**最小可用产品闭环**，先不处理 `.clawbox` 分发格式，聚焦“快速拉起可用 VM 并完成 Agent 初始化”。

核心结论：

1. `clawfarm` 当前阶段仅优先支持：`new` / `ps` / `suspend` / `resume`。
2. `clawfarm new <image>` 直接创建并启动一个新 VM 实例（分配 `CLAWID`），可被 `clawfarm ps` 看到。
3. `--run` 支持多次传入，按顺序在 VM 内以 `root` 权限执行，且在执行阶段提供交互式 shell 能力。
4. `--volume <VOLUME>:<GUEST_PATH>` 将宿主目录 `~/.clawfarm/claws/<CLAWID>/volumes/<VOLUME>` 挂载到客户机目标路径。
5. `.clawbox` 相关导入/导出、打包格式、层管理等暂不作为 P0/P1 必需能力。
6. `new` 默认不等待健康检查（等价 `--no-wait` 语义）；命令成功与否仅由启动阶段与 `--run` 执行 exit code 判定。

> 注：命令统一使用 `clawfarm`（你示例中的 `clarmfarm` 视为拼写笔误）。

---

## 2) Motivation

现阶段目标是尽快让用户完成如下路径：

- 指定基础镜像（如 `ubuntu:24.04`）
- 启动一个可持久化的 VM 实例
- 在 VM 内执行安装命令（如安装 openclaw）
- 挂载持久化目录（如 `/root/.openclaw`）
- 后续通过 `ps/suspend/resume` 管理生命周期

相比先做分发格式（`.clawbox`），上述路径更直接支撑“可运行、可迭代”的生产能力。

---

## 3) Goals

- 提供单命令创建实例：`clawfarm new <image-ref>`。
- 提供 `--run` 的顺序执行能力，支持交互式 root shell。
- 提供 `--volume` 的持久化目录挂载能力。
- 提供基础运行态管理：`ps`、`suspend`、`resume`。
- 保证实例状态、锁、元数据都在 `~/.clawfarm/claws/<CLAWID>/` 下。

## 4) Non-Goals (for this RFC)

- `.clawbox` 导入/导出/实时挂载。
- checkpoint/restore/rm 的产品打磨（可保留现有实现，但不作为本 RFC 交付目标）。
- 通用 VM 管理器能力（例如复杂网络、快照矩阵、跨 hypervisor 兼容）。

---

## 5) CLI UX Proposal

### 5.1 Example

```bash
clawfarm new ubuntu:24.04 \
  --run "curl -fsSL https://gitee.com/openclaw-mirror/install-script/raw/main/install.sh | bash" \
  --run "openclaw onboard --install-daemon" \
  --volume .openclaw:/root/.openclaw
```

### 5.2 Command surface (P0/P1)

- `clawfarm new <image-ref> [flags...]`
- `clawfarm ps`
- `clawfarm suspend <CLAWID>`
- `clawfarm resume <CLAWID>`

### 5.3 `new` flags (initial)

- `--name <name>`: 可选实例名前缀，用于生成 `CLAWID`。
- `--run <cmd>`: 可重复，按输入顺序执行。
- `--volume <volumeName>:<guestAbsPath>`: 可重复。
- `--cpus <n>` / `--memory-mib <n>` / `--port <n>`：沿用现有资源与网关参数。

---

## 6) Behavior Spec

### 6.1 `new` lifecycle

1. 解析镜像引用并准备基础镜像（命中缓存则不下载）。
2. 创建 `CLAWID` 与实例目录。
3. 初始化磁盘（从基础镜像复制/准备到实例目录）。
4. 挂载声明的 volume。
5. 启动 VM。
6. 顺序执行 `--run` 命令（root 权限，支持交互）。
7. 写入运行态元数据，`ps` 可见。

### 6.2 `--run` execution model

- `new` 通过 cloud-init 注入一次性实例级 SSH key，并确保安装/启用 `openssh-server`。
- `--run` 统一通过 SSH 登录 guest 执行（host `127.0.0.1:<ssh_port>` 映射到 guest `22`）。
- 多个 `--run` 按顺序执行，默认执行形式为 `bash -lc "<cmd>"`。
- 执行上下文为 `root`。
- 为执行阶段提供 TTY 交互能力（用户可输入、查看实时输出）。
- 任一 `--run` 命令失败时：
  - 交互模式下提供选择：`退出失败` / `进入救援 shell` / `忽略并继续后续 --run`。
  - 非交互模式下默认直接退出失败（返回非 0），并将实例状态置为 `unhealthy`（或 `error`，待实现统一）。

### 6.3 `--volume` mapping model

- 输入格式：`<VOLUME>:<GUEST_ABS_PATH>`。
- `VOLUME` 对应宿主路径：
  `~/.clawfarm/claws/<CLAWID>/volumes/<VOLUME>`
- 若目录不存在则自动创建。
- 以读写方式挂载到 guest 目标路径。
- 数据在 `suspend/resume` 后保持。

约束建议：

- `GUEST_ABS_PATH` 必须为绝对路径。
- `VOLUME` 仅允许 `[a-zA-Z0-9._-]`，禁止 `..` 与路径分隔符。

### 6.4 `new` completion semantics

- `new` 默认不阻塞等待 HTTP/应用级健康检查。
- `new` 的成败仅基于以下阶段的 exit code：
  - VM 启动与基础初始化阶段；
  - 用户声明的 `--run` 命令执行阶段。
- 当上述阶段都成功时，`new` 立即返回成功。

---

## 7) Runtime Layout

统一目录：`~/.clawfarm/claws/<CLAWID>/`

建议最小布局：

- `instance.json`：实例元数据（状态、PID、端口等）
- `state.json`：锁管理运行态（active/pid/source_path 等）
- `instance.flock`：实例互斥锁
- `instance.img`：实例磁盘
- `state/`：运行时状态目录
- `volumes/<VOLUME>/`：用户声明的持久化卷目录
- `serial.log` / `qemu.log`：运行日志

---

## 8) `ps` Output Expectations

`clawfarm ps` 至少展示：

- `CLAWID`
- `IMAGE`
- `STATUS`（booting/running/ready/suspended/unhealthy/exited）
- `GATEWAY`
- `PID`
- `UPDATED(UTC)`
- `LAST_ERROR`

`new` 创建后应立即可被 `ps` 观察到。

---

## 9) Compatibility and Migration

- 当前已有 `run` 命令可保留为内部/兼容入口，但产品主路径迁移到 `new`。
- 文档与示例统一以 `new/ps/suspend/resume` 为准。
- `.clawbox` 相关内容从主流程降级为后续议题。

---

## 10) Acceptance Criteria

1. 运行以下命令可成功创建实例并在 `ps` 中可见：

   ```bash
   clawfarm new ubuntu:24.04 \
     --run "echo ok > /root/new-ok" \
     --volume .openclaw:/root/.openclaw
   ```

2. `--run` 中可执行需要交互输入的命令（具备 TTY 交互通道）。
3. 宿主目录 `~/.clawfarm/claws/<CLAWID>/volumes/.openclaw` 与 guest `/root/.openclaw` 双向可见。
4. `suspend` 后 `resume`，实例可恢复，volume 数据不丢失。
5. 不依赖 `.clawbox` 能完成完整新建与管理流程。

---

## 11) Open Questions

当前版本无阻塞性 open question。
