# clawfarm

`clawfarm` is a VM sandbox runtime for AI agents, prioritizing guest systems with GUI capabilities.

> ⚠️ Early stage project: interfaces, file formats, and workflows may change quickly.

## Positioning

- **Goal:** run and manage agent-centric VM sandboxes where the guest system itself is GUI-capable.
- **Non-goal:** become a general-purpose VM manager; `clawfarm` is built with AI agents as first-class users.

## Usage

```bash
make build

./clawfarm new # an interactive TUI to guide you for creating a new .clawbox file
./clawfarm run demo.clawbox --name demo-a # run the .clawbox file
./clawfarm ps
./clawfarm stop <CLAWID>
```
