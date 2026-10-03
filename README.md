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

## Development / Validation

Windows continua sendo o ambiente de desenvolvimento normal. Linux via
Docker é o ambiente oficial de validação Go, usando `golang:1.25`, alinhado
ao `go.mod`. O Smart App Control pode bloquear executáveis temporários de
teste Go gerados localmente no Windows.

Validação completa rápida, sem rebuild, no PowerShell (uma linha):

```powershell
docker run --rm -v "${PWD}:/app" -w /app golang:1.25 ./scripts/validate.sh
```

Para validar usando a imagem construída:

```powershell
docker build -t dev-orchestrator-validation .
docker run --rm dev-orchestrator-validation ./scripts/validate.sh
```

O script executa `go test ./...`, `go vet ./...` e `go build ./...`, nessa
ordem, e para imediatamente se algum comando falhar. A imagem serve apenas
para desenvolvimento/validação; o runtime do Local Agent será decidido no P2.

## Primeiro passo

Não implemente integrações ainda. Comece em `P0.1`, refinando uma task
por vez segundo `docs/TASK_SPEC.md`.
