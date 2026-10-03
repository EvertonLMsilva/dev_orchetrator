# Roadmap operacional

## Meta do MVP

Concluir P0--P5 e validar o ciclo em um projeto piloto.

  --------------------------------------------------------------------------------
  Fase                    Estado inicial          Gate
  ----------------------- ----------------------- --------------------------------
  P0 Fundação             PLANNED                 domínio/protocolo/persistência
                                                  mínimos

  P1 Discord              PLANNED                 comando chega ao Orchestrator e
                                                  resposta volta

  P2 Agente Local         PLANNED                 evidência read-only segura

  P3 Planner              PLANNED                 análise → BOT_COMMAND/CODEX_TASK
                                                  estruturados

  P4 Executor             PLANNED                 task fechada →
                                                  mudança/teste/result

  P5 Orquestração         PLANNED                 ciclo completo controlado

  P6 Git                  FUTURE                  branch/commit/push/PR
                                                  controlados

  P7 Segurança avançada   FUTURE                  hardening baseado em uso real

  P8 Multiprojeto         FUTURE                  isolamento comprovado

  P9 Autonomia controlada FUTURE                  budgets/loops/escalation
                                                  comprovados
  --------------------------------------------------------------------------------

## Dependência

`P0 → P1/P2 → P3 → P4 → P5 → P6/P7 → P8 → P9`

P1 e P2 podem avançar após os contratos necessários de P0.

## Discovery obrigatório

P3 começa verificando interfaces oficiais atuais para integração
programática do Planner. P4 começa verificando interfaces oficiais
atuais para acionamento do Codex/Executor. Não desenhar adapter concreto
com base em suposição.

## Regra

Executar somente a menor task desbloqueada. Detalhes estão em
`docs/tasks/P*.md`.
