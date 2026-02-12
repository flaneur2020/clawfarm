# RFC 005 — Clawbox 挂载生命周期与单锁模型（Simplified）

- **Status:** Draft
- **Date:** 2026-02-10
- **Project:** `clawfarm`
- **Related:**
  - `rfc/004-clawbox-mountable-single-file-format.md`

## 1. 摘要

本 RFC 定义 M2 的最小可实现方案：

1. 使用**单一锁文件**：`~/.clawfarm/claws/{CLAWID}/instance.flock`；
2. 锁实现固定使用 `github.com/gofrs/flock`；
3. 挂载路径固定：`~/.clawfarm/claws/{CLAWID}/mount`（只读）；
4. “是否已打开”只由 `flock` 判定，`state.json` 仅用于状态展示。

---

## 2. 目标

- **G1:** 同一 `CLAWID` 并发操作不会竞争写坏状态；
- **G2:** 同一 `CLAWID` 同时只能运行一个实例；
- **G3:** 设计足够简单，优先快速落地 M2。

---

## 3. 目录结构

```text
~/.clawfarm/
  claws/
    <CLAWID>/
      mount/            # 只读挂载点
      instance.flock    # 唯一锁文件（gofrs/flock）
      state.json        # 运行状态（用于展示，不作为占用真值）
```

---

## 4. 核心规则

## 4.1 单锁规则

- 所有会修改该 `CLAWID` 状态的操作都必须先 `TryLock`：
  - `run`
  - `rm`
  - `export`
  - `checkpoint`
  - `restore`
- 使用非阻塞锁（`TryLock`），拿不到锁就直接失败并提示“busy”。

## 4.2 单实例判定规则

- `run` 的“已打开”判定只看是否拿到 `instance.flock`；
- 只要能拿到锁，就允许继续；
- 不允许因 `state.json.active=true` 直接失败。

## 4.3 状态文件规则

- 启动成功后写 `state.json.active=true`；
- 实例停止/删除后写 `state.json.active=false`；
- `state.json` 可能因异常退出而过期，不用于互斥判断。

## 4.4 挂载规则

- `run` 过程中在锁保护下挂载到 `.../{CLAWID}/mount`；
- 若已挂载且来源一致，可复用；
- 目录必须只读。

---

## 5. 生命周期（最小流程）

## 5.1 `run`

1. `TryLock(instance.flock)`；
2. 执行只读挂载（或复用挂载）；
3. 启动实例；
4. 写 `state.json.active=true`（展示用途）；
5. 释放锁。

## 5.2 `stop/rm`

1. `TryLock(instance.flock)`；
2. 停止实例；
3. 卸载 `mount/`（若需要）；
4. 写 `state.json.active=false`（展示用途）；
5. 释放锁。

## 5.3 `recover`（内部逻辑，可先不暴露命令）

- 在锁内检测状态残留（例如 `state.json.active=true` 但实例已不存在）时：
  - 清理残留挂载（若有）；
  - 回写 `active=false`。

---

## 6. Go 实现建议

建议新增 `internal/mount`：

```go
type Manager interface {
    Acquire(ctx context.Context, req AcquireRequest) error
    Release(ctx context.Context, req ReleaseRequest) error
    Recover(ctx context.Context, clawID string) error
}
```

建议锁抽象：

```go
type Locker interface {
    TryLock(path string) (Unlock func() error, ok bool, err error)
}
```

默认实现：`github.com/gofrs/flock`。

---

## 7. 错误模型（最小集）

- `ErrBusy`：当前 `CLAWID` 正在被并发操作；
- `ErrMountConflict`：挂载来源不一致；
- `ErrInvalidState`：状态文件损坏。

---

## 8. 验收标准

1. 并发 `run` 同一 `CLAWID` 时只有一个成功；
2. 挂载路径固定为 `~/.clawfarm/claws/{CLAWID}/mount` 且只读；
3. 锁实现固定使用 `github.com/gofrs/flock`；
4. `state.json` 仅用于状态展示，不参与占用判定。

---

## 9. 待决问题（压缩版）

1. `state.json` 字段是否复用现有 instance metadata；
2. `recover` 是否在 M2 就暴露为用户命令。
