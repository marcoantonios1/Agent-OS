# Agent-OS
A multi-agent AI system with a single entry point that routes requests to specialized agents (assistant, builder, research) powered by Costguard.

## Architecture

```
Channels (web / discord / whatsApp / telegram)
              │
              ▼
         Router / App
              │
    ┌─────────┼─────────┐
    ▼         ▼         ▼
 Comms     Builder   Research
 Agent      Agent     Agent
              │
    ┌─────────┼─────────┐
    ▼         ▼         ▼
 Memory  Orchestration CostGuard
              │
    ┌─────────┼─────────┐
    ▼         ▼         ▼
 Email    Calendar   WebSearch
  Tool      Tool       Tool
```
