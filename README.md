# clawfarm（当前 CLI 名称：`vclaw`）

一个用于运行 OpenClaw 的轻量 VM 工具，当前后端是 QEMU。

## 与 RFC 对齐（004/005）

当前实现重点对齐：

- `.clawbox` 单文件输入；
- 运行时挂载语义与 `CLAWID` 生命周期管理；
- 单锁模型：`instance.flock`（`gofrs/flock`）；
- `state.json` 仅用于状态展示，不作为互斥真值；
- JSON-spec 模式下，artifact 按内容寻址缓存到：
  - `~/.clawfarm/blobs/<sha256>`。

## 当前支持能力

- 命令：`run`, `image`, `ps`, `suspend`, `resume`, `rm`, `export`, `checkpoint`, `restore`。
- `run` 支持两种 `.clawbox`：
  1. **header-json**：`run <file.clawbox>` / `run .`（目录唯一文件）；
  2. **spec-json（早期简化模式）**：首字符是 `{` 时按 spec 解析，不走 mount，下载 `base_image/layers`，校验 SHA256，执行 `provision`。
- OpenClaw 参数 preflight：缺参时交互引导；非交互模式直接失败。
- `ps` 展示健康状态：`ready / unhealthy / exited` 与 `last_error`。

## 目录与缓存

- 主目录（可由环境变量覆盖）：
  - `CLAWFARM_HOME`
  - `CLAWFARM_CACHE_DIR`
  - `CLAWFARM_DATA_DIR`
- JSON-spec 下载流程：
  1. 先下载到临时文件；
  2. 校验 SHA256；
  3. `rename` 为 `~/.clawfarm/blobs/<sha256>`；
  4. 若 `<sha256>` 已存在且校验通过，直接复用，不重复下载。

## 快速开始

```bash
make build

./vclaw image ls
./vclaw image fetch ubuntu:24.04
./vclaw run demo.clawbox --workspace=. --no-wait
./vclaw ps
./vclaw rm <CLAWID>
```

## 测试

```bash
make test
make integration
```
