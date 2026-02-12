# clawfarm

`clawfarm` is a **GUI-first VM sandbox** for AI agents.

> ⚠️ Early stage project: interfaces, file formats, and workflows may change quickly.

## Positioning

- **Goal:** run and manage agent-centric VM sandboxes with a GUI-first product direction.
- **Non-goal:** become a general-purpose VM manager.

## Usage

```bash
make build

./clawfarm new
./clawfarm run demo.clawbox --name demo-a
./clawfarm ps
./clawfarm stop <CLAWID>
```

## Note

The CLI and workflows are evolving rapidly in this stage.
