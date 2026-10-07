# Roadmap operacional

## MCP — camada READ interna

| Entrega | Estado | Escopo |
| --- | --- | --- |
| MCP-1 — READ contracts/validation | DONE — aprovado pelo Planner | DTOs e validação estrita em `internal/application/readcontracts`. |
| MCP-2 — READ Application Boundary | DONE — aprovado pelo Planner | Dispatch interno das quatro operações aprovadas, capacidades READ existentes e projeções MCP-1. |
| MCP-3 — Identity & Authorization Boundary | DONE — aprovado pelo Planner | AuthenticationPort, GrantRepository por requisição e grant exato Principal/Operation/Project antes do MCP-2. |
| MCP-4 — Secure READ Runtime | DONE — aprovado pelo Planner | Auth adapter + Grants adapter + Audit fail-closed + composição segura interna. |
| MCP-5 — External MCP Adapter & Runtime | Implementado e validado; pendente de review | Inbound adapter MCP no próprio Orchestrator, runtime existente, Docker, lifecycle/readiness e cliente MCP local real. |
| MCP-6 — External READ E2E Completion | DONE — aprovado pelo Planner; EXTERNAL_E2E=PASS | ChatGPT/Secure MCP Tunnel → READ real; quatro tools, negação de projeto não autorizado e durable audit aprovados. AVAILABLE permanece capacidade configurada, não health operacional. |
| MCP-7 — Production MCP Runtime | IMPLEMENTATION_COMPLETE; LOCAL_VALIDATION_COMPLETE; EXTERNAL_E2E_PENDING | Compose oficial, security em volume Linux, operação PowerShell e persistência local validados; não DONE. |
| MCP-8 | NEXT | Não iniciado; aguarda fechamento externo do MCP-7 e refinamento do Planner. |

Especificação e limites: [MCP](tasks/MCP.md). MCP-5 foi provado com cliente local;
não houve exposição pública ou conexão ChatGPT. O inbound adapter integra o mesmo
processo/runtime do Orchestrator; não há Gateway independente. MCP-6 comprovou
E2E externo real via OpenAI Secure MCP Tunnel, com aceite aprovado pelo Planner.
MCP-7 aguarda aceite E2E externo do Planner. MCP-8 permanece NEXT.

## Meta do MVP — registro histórico (estado atual abaixo)

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

## Regra histórica (granularidade atual abaixo)

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

## Estado operacional reconciliado — 2026-10-06

Este quadro substitui os estados iniciais/históricos acima. O fechamento P5
continua válido como registro daquele momento; P6 já foi implementado parcialmente.

| Tópico/capacidade | Estado atual | Gate restante |
| --- | --- | --- |
| P0–P4 | Implementação DONE conforme histórico | LIVE autenticado P4 não declarado resolvido |
| P5 Orquestração | Application DONE | B-P5-001: composição de produção ainda indisponível |
| P6 Authenticated Provider Runtime | IN_PROGRESS, branch/PR única | LIVE Planner → composição read-only → Discord E2E real |
| Mediated WRITE / Git controlado (antigo P6 Git) | POSTERIOR | Mediação real e aprovações verificáveis; fora do gate E2E read-only |
| P7–P9 | FUTURE | Refinamento posterior; não antecipar implementação |

Fonte única da reconstrução, commits, subdivisões históricas, invariantes,
componentes prováveis, testes, aceites e STOP conditions: docs/tasks/P6.md.
P6.3d DONE em 70cf070 fecha somente WRITE simbólico. PlannerWriteDispatcher não
medeia efeitos reais do Codex; filesystem/git WRITE real = DENY.

Já implementados: fonte ChatGPT autorizada/sessão Docker, bootstrap device-code,
persistência runtime-owned, account/read e diagnóstico seguro, adapter Planner
concreto, runtime autenticado sem ferramentas e contrato WRITE simbólico.
Implementação e harness opt-in não constituem prova de sucesso LIVE. main vazio
impede composição operacional; teste LIVE local não rastreado não é entrega aceita.
A causa histórica de B-P5-001 (ausência de Planner concreto) foi parcialmente
superada; o blocker de produção permanece até fechamento dos gates P6.

## Caminho restante até primeiro LIVE e Discord real

| Entrega funcional | Dependências | Resultado verificável |
| --- | --- | --- |
| P6.4 Inferência autenticada LIVE controlada | Runtime atual, auth runtime-owned, Docker/Linux | Uma inferência real sem ferramentas, decisão válida, persistência/cleanup PASS e regressão GREEN |
| P6.5 Composição operacional segura/read-only | P6.4 aceita, contratos P5, configuração autorizada | Entrypoint real e gate que impede Executor com efeitos e filesystem/git WRITE |
| P6.6 Discord E2E real seguro/read-only | P6.4/P6.5 aceitas e autorização explícita do ensaio | Discord → Planner → evidência permitida → resposta correlacionada, sem mutação de projeto/Git |

Distância: três entregas coesas; P6.4 comprova primeiro LIVE do Planner, P6.5
libera iniciar Discord E2E, P6.6 comprova o ciclo real. Não exigir mediated WRITE
para esse ensaio. Pedido de WRITE/PREPARE_EXECUTOR deve bloquear/escalar antes
que alcance execução com efeitos. Não há autorização LIVE nesta atualização.

P6 DONE exige os três aceites/evidências aprovados pelo Planner, regressão oficial
./scripts/validate.sh Linux/Docker GREEN e reconciliação de B-P5-001 no escopo de
runtime/composição. Não exige escrever uma task piloto nem operações Git reais.
Mediated WRITE permanece capacidade posterior; objetivos, invariantes e STOP
conditions estão em P6.md, sem reativar o antigo catálogo como tasks atuais.

## Granularidade vigente

Tópico pai = grande capacidade; subtópico = entrega funcional coesa.
Discovery/RED/GREEN/implementação/regressão são etapas internas. Não criar task
por teste, arquivo ou fase; autorização deve cobrir o ciclo completo refinado
segundo TASK_SPEC. Preferir término em capacidade verificável, regressão GREEN
e commit significativo. Uma capacidade por execução, sem refatoração lateral.
P6 inteiro = uma branch/PR; subdivisões históricas são rastreabilidade.
Dependência operacional atual: P5 application DONE → P6.4 → P6.5 → P6.6;
mediated WRITE/Git, P7–P9 ficam posteriores e dependem de refinamento do Planner.

## P10 — estado atual após implementação no PR #7

Este registro substitui a indicação histórica de mediated WRITE/Git como
capacidade posterior acima, no escopo controlado de P10. P10 permanece
IN_PROGRESS; não há declaração de LIVE-ready ou aceite final de P10.4.

| Subtópico | Estado atual |
| --- | --- |
| P10.1 | CLOSED — PLANNER_REVIEW APPROVED |
| P10.2 | CLOSED — PLANNER_REVIEW APPROVED |
| P10.3 | CLOSED — PLANNER_REVIEW APPROVED |
| P10.4 | IMPLEMENTED — PLANNER_REVIEW_PENDING |
| P10.5 | NOT_STARTED |
| P10.6 | NOT_STARTED |

Evidências e blockers históricos: [P10](tasks/P10.md). O blocker de discovery
de provisionamento/metadata Git foi superado pela implementação de P10.4;
o aceite do Planner permanece pendente.
