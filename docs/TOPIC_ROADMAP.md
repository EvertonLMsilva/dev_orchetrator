# Roadmap operacional

## Meta do MVP

Concluir P0--P5 e validar o ciclo em um projeto piloto.

  --------------------------------------------------------------------------------
  Fase                    Estado inicial          Gate
  ----------------------- ----------------------- --------------------------------
  P0 Fundação             DONE                    domínio/protocolo/persistência
                                                  mínimos

  P1 Discord              DONE                    comando chega ao Orchestrator e
                                                  resposta volta

  P2 Agente Local         DONE                    evidência read-only segura

  P3 Planner              DONE                    análise → BOT_COMMAND/CODEX_TASK
                                                  estruturados

  P4 Executor             PLANNED                 CODEX_TASK → Executor →
                                                  CODEX_RESULT

  P5 Orquestração         PLANNED                 ciclo completo controlado

  P6 Git                  FUTURE                  branch/commit/push/PR
                                                  controlados

  P7 Segurança avançada   FUTURE                  hardening baseado em uso real

  P8 Multiprojeto         FUTURE                  isolamento comprovado

  P9 Autonomia controlada FUTURE                  budgets/loops/escalation
                                                  comprovados
  --------------------------------------------------------------------------------

P0–P3 concluídos. P4 Executor é o próximo tópico; P5 permanece PLANNED.

## Dependência

`P0 → P1/P2 → P3 → P4 → P5 → P6/P7 → P8 → P9`

P1 e P2 podem avançar após os contratos necessários de P0.

## Discovery obrigatório

P3 começa verificando interfaces oficiais atuais para integração
programática do Planner. P4 começa verificando interfaces oficiais
atuais da OpenAI/Codex antes de qualquer adapter concreto. Não assumir
antecipadamente API, SDK, CLI, app-server, exec-server, autenticação, sessão ou protocolo.

## Regra

Executar somente a menor task desbloqueada. Detalhes estão em
`docs/tasks/P*.md`.
