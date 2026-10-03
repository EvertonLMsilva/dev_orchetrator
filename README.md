# Dev Orchestrator

Orquestrador de desenvolvimento controlado por Discord entre:

-   operador humano;
-   ChatGPT/Planner;
-   agente local no PC;
-   Codex/Executor.

O objetivo é reduzir trabalho manual e consumo de contexto: o Planner
investiga e decide; o agente local coleta evidências mínimas; o Codex
recebe somente uma task fechada, implementa e testa; o Planner revisa e
decide o próximo passo.

## Regra central

**Discord é interface/transporte. O estado verdadeiro pertence ao
Orchestrator e ao repositório.**

Não assuma que mencionar `@ChatGPT` ou `@Codex` no Discord aciona
produtos nativamente. As integrações programáticas serão validadas antes
da implementação.

## Estrutura

``` text
AGENTS.md
docs/
  PROJECT_VISION.md
  ARCHITECTURE.md
  PROTOCOL.md
  SECURITY.md
  PROJECT_WORKFLOW.md
  TASK_SPEC.md
  TOPIC_ROADMAP.md
  DECISIONS.md
  ERROR_MODEL.md
  OBSERVABILITY.md
  CONFIGURATION.md
  tasks/P0.md ... P9.md
  templates/
```

## MVP

O MVP termina em **P5 --- Orquestração**.

Até lá o sistema deve conseguir:

`Discord → Planner → agente local → Planner → Codex → testes → Planner review → DONE/BLOCKED → Discord`.

P6--P9 evoluem Git/PR, segurança, multiprojeto e autonomia controlada.

## Primeiro passo

Não implemente integrações ainda. Comece em `P0.1`, refinando uma task
por vez segundo `docs/TASK_SPEC.md`.
