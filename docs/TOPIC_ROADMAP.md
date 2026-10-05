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

  P4 Executor             DONE                    CODEX_TASK → Executor →
                                                  CODEX_RESULT

  P5 Orquestração         NEXT                    ciclo completo controlado

  P6 Git                  FUTURE                  branch/commit/push/PR
                                                  controlados

  P7 Segurança avançada   FUTURE                  hardening baseado em uso real

  P8 Multiprojeto         FUTURE                  isolamento comprovado

  P9 Autonomia controlada FUTURE                  budgets/loops/escalation
                                                  comprovados
  --------------------------------------------------------------------------------

P0–P4 concluídos. P4 implementation DONE; P5 — Orquestração é NEXT.
Execução live autenticada P4 permanece BLOCKED por B-P4-001.

## Estado dos tópicos P4

| Tópico | Estado |
| --- | --- |
| P4.1 Official Codex Integration Discovery + ADR | DONE |
| P4.2 Executor Contracts + ExecutorPort | DONE |
| P4.3 Execution Package + Policy | DONE |
| P4.4 Codex Provider Adapter | DONE |
| P4.5 Result Normalization + CODEX_RESULT | DONE |
| P4.6 ExecutorSession, Limits + Integrated Validation | DONE |

P4.4: Implementation and deterministic/unauthenticated validation complete.
Authenticated live execution remains blocked by B-P4-001.
TD-P4-001 remains open.

Review P4.4.13: `P4.4_REVIEW=PASS`; implementação P4.4 fechada após a
composição concreta de sessão Docker (`5cf7e9829eba91b93528ab9eeedffb767e9e5773`).
`B-P4-001=OPEN`, `TD-P4-001=OPEN`, `B-P4-002=NOT_NEEDED`.
Isso não comprova startup/thread/turn autenticados nem execução live de task.

Review P4.5.3: `P4.5_REVIEW=PASS`, `implementation=COMPLETE`,
`validation=COMPLETE`. Contrato tipado e codec estrito reutilizam
`domain.Envelope`, preservam ProjectID/TaskID e isolam Outcome de Summary;
DONE/BLOCKED/FAILED, provider-independent e fail closed. P4.5 não depende
de live Codex/auth; `B-P4-001=OPEN`, `TD-P4-001=OPEN` e
`B-P4-002=NOT_NEEDED` permanecem.

Review P4.6.4: `P4_REVIEW=PASS`, `previous_finding_resolved=yes`.
Lifecycle descartável, limites e validação integrada COMPLETE;
`ExecutorSession != TaskState`, CANCELLED `KEEP_SESSION_ONLY`.
Histórico P4.6.1–P4.6.3b e evidência detalhada em `docs/tasks/P4.md`.
P4 implementation DONE; deterministic validation COMPLETE;
unauthenticated live validation COMPLETE pela evidência anterior aceita;
authenticated live validation BLOCKED, sem execução autenticada validada.
`B-P4-001=OPEN`, `TD-P4-001=OPEN`, `B-P4-002=NOT_NEEDED`.
Validação Linux/Docker: testes completos/focados, vet, build, race,
`./scripts/validate.sh` e `git diff --check` PASS.
Sincronização documental P4 concluída conforme `docs/tasks/P4.md`.
P5 é somente NEXT; nenhuma implementação de Orquestração neste fechamento.

Final Pre-PR Review após P4.6.5–P4.6.7: `STATUS=DONE`,
`P4 technical review=PASS`, `F1=RESOLVED`, `F2=RESOLVED`, `F3=RESOLVED`,
`FINDINGS=none`. Histórico das correções e validações em `docs/tasks/P4.md`.
P4 permanece DONE; P5 permanece NEXT. Validação live autenticada permanece
BLOCKED; `B-P4-001=OPEN`, `TD-P4-001=OPEN`, `B-P4-002=NOT_NEEDED`.

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

## Fechamento P5 — estado atual

Esta atualização substitui o estado histórico P5=NEXT acima.

| Escopo | Estado |
| --- | --- |
| P5 application orchestration (P5.1a/b/c, P5.2a) | DONE |
| Production authenticated runtime / Discord operational composition | BLOCKED — B-P5-001 |

Production composition blocked by B-P5-001. Registro único do blocker,
causa raiz, cobertura integrada e limites do fechamento em `docs/tasks/P5.md`.
B-P4-001 permanece relacionado; não há Planner fake em produção nem fallback de auth.

Próximo tópico recomendado: **P6 — Authenticated Provider Runtime**.
Fonte ChatGPT autorizada → app-server autenticado → Planner concreto → composition
root → Discord operacional E2E. A prioridade/numeração do tópico Git histórico
fica para o Planner. Nenhum runtime autenticado foi implementado neste fechamento.
