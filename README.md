# vclaw

Run full OpenClaw inside a lightweight VM powered by vz.

You can easily manage claws in lightweight virtual machines, and let claws work together.

## Current milestone status

This repository is in active development:

- RFC drafted in `rfc/001-initial-design.md`
- `vclaw` CLI commands present: `run`, `image`, `ps`, `suspend`, `rm`
- disk-image flow supports Ubuntu community cloud images (Lima-template style)

```
vclaw image ls
vclaw image fetch ubuntu:24.04
vclaw run ubuntu:24.04 --workspace=. --port-forward=8080:80
vclaw ps
vclaw suspend <CLAWID>
vclaw resume <CLAWID>
vclaw rm <CLAWID>
```
