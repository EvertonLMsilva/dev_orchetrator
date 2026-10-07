# Roadmap operacional

## MVP — estado vigente

Reconciliação documental em 2026-10-07, conforme aceites do Planner e registros
[P5](tasks/P5.md), [P6](tasks/P6.md), [P10](tasks/P10.md) e [MCP](tasks/MCP.md).
Esta seção é canônica; os blocos HISTÓRICO/SUPERSEDED abaixo preservam evidência
e não reabrem entregas aceitas nem concedem autorização para novas execuções.

MVP funcional ainda não aceito: falta provar uma tarefa de desenvolvimento em
UM projeto piloto pelo ciclo Usuário/Channel → Orchestrator → Planner → task
controlada → Executor → alteração mediada → Git local controlado → evidência/
resultado → usuário/Channel. O E2E P6.6 é READ-ONLY; MCP READ não executa esse ciclo.

| Capacidade | Classificação | Evidência ou gate vigente |
| --- | --- | --- |
| P0–P4: contratos, Channel, READ, Planner e Executor determinístico | DONE | Implementações aceitas; não equivale a Executor LIVE autenticado. |
| P5: orquestração de aplicação | DONE | Fechamento técnico preservado; composição operacional comprovada por P6. |
| P6: runtime autenticado do Planner e Discord READ E2E | DONE | P6.4/P6.5/P6.6 CLOSED; B-P5-001 fechado somente no escopo runtime/composição. |
| MCP READ operacional | DONE | MCP-7 DONE — aprovado pelo Planner; EXTERNAL_E2E=PASS. Review isolado MCP-5 permanece conforme seu registro, sem novo aceite inferido. |
| P10.1–P10.3: candidato, autorização e aplicação mediada recuperável | DONE | CLOSED — PLANNER_REVIEW APPROVED; prova determinística, sem provider concreto/LIVE ou composição produtiva aceitos. |
| P10.4: aceite do Git local controlado | DONE | CLOSED — PLANNER_REVIEW APPROVED em 2026-10-07; Git local aceito. |
| Ciclo de desenvolvimento LIVE integrado e seguro | MVP_GATE | MVP-2 implementa provider concreto, ator/grants/issuance e composição com confirmações separadas; validação/evidência em P10. E2E LIVE continua pendente. |
| Executor LIVE pelo adapter legado P4 | BLOCKED | B-P4-001 OPEN nesse caminho; não declarado resolvido por P6 ou por implementação P10. |
| MCP-8, P10.5/P10.6 e P7–P9 | POST_MVP | Não indispensáveis ao primeiro ciclo local; não iniciados por esta reconciliação. |
| TD-P4-001 e histórico B-P4-001 para o caminho legado | DEBT_NON_BLOCKING | Continuam abertos no escopo legado; não bloqueiam automaticamente MODEL_D. A prova LIVE de MODEL_D continua sendo MVP_GATE. |

Invariantes do MVP: autorização explícita por ator autenticado/autorizado,
fail-closed, workspace confinado e single-writer, WRITE mediado, gates Git
separados, auditoria/evidência correlacionadas, limites e possibilidade de BLOCK/
escalation. Channel não emite autoridade; nem ALLOW textual, provider DONE ou
WRITE simbólico autorizam efeitos. Executor não recebe workspace real/.git;
MODEL_D produz proposta estrita sem ferramentas, aplicada por componentes trusted.

## Caminho mínimo restante para MVP

MVP_REMAINING_DELIVERIES: 1

### MVP-1 — Aceite do Git local controlado existente

Estado: DONE — P10.4 CLOSED — PLANNER_REVIEW APPROVED, decisão explícita do Planner.
Dependências: P10.1–P10.3 aceitos; implementação/testes P10.4 no PR #7, bfe8870.
Prova de aceite: review do Planner confirma que provisionamento/baseline e
metadata registrados preservam a autoridade do ManagedWorkspace e que branch/
commit locais possuem autorização exata, efeitos limitados ao candidato aplicado,
evidência e rejeição de replay/divergência/falha. O escopo implementado cobre as
operações locais necessárias; sua suficiência segura depende desse aceite,
não de push/PR/merge. Só um gap concreto do review justificará implementação
adicional; o blocker antigo de discovery Git não é reaberto.

### MVP-2 — Ciclo LIVE de desenvolvimento em um piloto controlado

Estado: MVP_GATE — IMPLEMENTATION_COMPLETE; LOCAL_VALIDATION_COMPLETE;
LIVE_E2E_PENDING. MVP permanece PENDING.
Dependências: MVP-1 aceito; P5/P6 e P10.1–P10.3 preservados; task refinada e
autorização explícita do Planner antes de implementação/ensaio LIVE. Provider
CandidateGenerator concreto tool-free, configuração trusted de actor/grants e
approval issuer, além da composição com ManagedWorkspace, estão implementados
no MVP-2. Isso não substitui o aceite da prova LIVE. Não reutilizar o Executor legado
bloqueado nem habilitar WRITE na composição READ-ONLY P6 como atalho.
Prova de aceite: uma solicitação real no Channel origina task/IDs confiáveis;
Planner prepara a tarefa e o Executor MODEL_D autenticado gera candidato em cópia
independente com TOOLS=EMPTY. Aprovação contextual por ator autorizado permite
WRITE_APPLY exato pelo mediated applier, seguido de branch/commit locais sob seus
gates próprios. Resultado, hashes/recibos Git e evidência correlacionada retornam
ao usuário, com TaskState controlado pelo WorkflowEngine e sem depender do summary
do modelo para declarar sucesso. Comprovar no mesmo aceite negação sem efeitos
para ausência/divergência/replay de autorização, BLOCK/escalation, limites,
persistência e shutdown/cleanup; regressão oficial Linux/Docker GREEN.

A documentação P10 registra ports/pipeline/candidato, store/applier recuperável
e Git local; isso permite agrupar provider concreto, composição e ensaio numa
única entrega E2E, sem declarar que já estão ligados. Refinamento deve delimitar
os arquivos e a autoridade do piloto provisionado, sem importar/adotar workspace
externo nem criar outra arquitetura. Se essa integração revelar dependência
indispensável não documentada, retornar ao Planner com o boundary exato.

B-P4-001 continua OPEN para o adapter legado; não é evidência de falha no novo
provider tool-free ainda não provado. Se o piloto depender do caminho legado,
esse blocker volta a ser dependência bloqueante explícita. Nenhum LIVE, nova
implementação ou mudança de contrato é autorizado por este roadmap.

## Pós-MVP

- P10.5: push remoto e publicação/PR controlados; NOT_STARTED, não autorizados.
- P10.6: merge explicitamente autorizado; NOT_STARTED, não autorizado.
- MCP-8: POST_MVP; permanece NEXT no tópico MCP, não iniciado nem automaticamente
  autorizado. MCP-7 satisfaz a necessidade READ já aceita; MCP WRITE não é requisito
  para usar um Channel autorizado no piloto de desenvolvimento.
- P7–P9: hardening avançado, multiprojeto e autonomia contínua, após refinamento.

Push, PR e merge não são gates do piloto local. Reparos do caminho Executor
legado/TD-P4-001 permanecem dívida restrita ao escopo afetado; não equivalem a
aceite de Executor LIVE nem justificam reabrir P6 ou B-P5-001.

## MCP — camada READ interna

| Entrega | Estado | Escopo |
| --- | --- | --- |
| MCP-1 — READ contracts/validation | DONE — aprovado pelo Planner | DTOs e validação estrita em `internal/application/readcontracts`. |
| MCP-2 — READ Application Boundary | DONE — aprovado pelo Planner | Dispatch interno das quatro operações aprovadas, capacidades READ existentes e projeções MCP-1. |
| MCP-3 — Identity & Authorization Boundary | DONE — aprovado pelo Planner | AuthenticationPort, GrantRepository por requisição e grant exato Principal/Operation/Project antes do MCP-2. |
| MCP-4 — Secure READ Runtime | DONE — aprovado pelo Planner | Auth adapter + Grants adapter + Audit fail-closed + composição segura interna. |
| MCP-5 — External MCP Adapter & Runtime | Implementado e validado; pendente de review | Inbound adapter MCP no próprio Orchestrator, runtime existente, Docker, lifecycle/readiness e cliente MCP local real. |
| MCP-6 — External READ E2E Completion | DONE — aprovado pelo Planner; EXTERNAL_E2E=PASS | ChatGPT/Secure MCP Tunnel → READ real; quatro tools, negação de projeto não autorizado e durable audit aprovados. AVAILABLE permanece capacidade configurada, não health operacional. |
| MCP-7 — Production MCP Runtime | DONE — aprovado pelo Planner; IMPLEMENTATION_COMPLETE; LOCAL_VALIDATION_COMPLETE; EXTERNAL_E2E=PASS | Startup oficial, build, readiness, Tunnel foreground e quatro READs externos PASS; negação de projeto não autorizado, durable audit persistente após restart e shutdown Exited (0) aprovados. |
| MCP-8 | NEXT | Não iniciado; aguarda refinamento do Planner. |

Especificação e limites: [MCP](tasks/MCP.md). MCP-5 foi provado com cliente local;
não houve exposição pública ou conexão ChatGPT. O inbound adapter integra o mesmo
processo/runtime do Orchestrator; não há Gateway independente. MCP-6 comprovou
E2E externo real via OpenAI Secure MCP Tunnel, com aceite aprovado pelo Planner.
MCP-7 teve aceite operacional e E2E externo aprovados pelo Planner; evidências
registradas na [task MCP-7](tasks/MCP.md#mcp-7--production-mcp-runtime).
MCP-8 permanece NEXT, não iniciado.

## Meta do MVP — HISTÓRICO/SUPERSEDED

Estado inicial preservado; substituído por **MVP — estado vigente** acima.

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

## Estado dos tópicos P4 — histórico de implementação/review preservado

As referências a P5=NEXT neste bloco são HISTÓRICO/SUPERSEDED. B-P4-001 e
TD-P4-001 não têm evidência de fechamento; seu escopo vigente está no quadro MVP.

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

## Dependência — HISTÓRICO/SUPERSEDED pelo caminho mínimo MVP

`P0 → P1/P2 → P3 → P4 → P5 → P6/P7 → P8 → P9`

P1 e P2 podem avançar após os contratos necessários de P0.

## Discovery obrigatório — histórico dos gates P3/P4

P3 começa verificando interfaces oficiais atuais para integração
programática do Planner. P4 começa verificando interfaces oficiais
atuais da OpenAI/Codex antes de qualquer adapter concreto. Não assumir
antecipadamente API, SDK, CLI, app-server, exec-server, autenticação, sessão ou protocolo.

## Regra histórica (granularidade atual abaixo)

Executar somente a menor task desbloqueada. Detalhes estão em
`docs/tasks/P*.md`.

## Fechamento P5 — HISTÓRICO/SUPERSEDED no escopo runtime/composição

Esta atualização substitui o estado histórico P5=NEXT acima.
O fechamento de aplicação permanece válido; B-P5-001 foi posteriormente fechado
por P6.4–P6.6, conforme o estado vigente acima e P6.md.

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

## Estado operacional — HISTÓRICO/SUPERSEDED (2026-10-06, antes do aceite P6)

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

## Primeiro LIVE e Discord real — HISTÓRICO/SUPERSEDED

As três entregas P6.4/P6.5/P6.6 abaixo foram concluídas/aceitas. Não são gates
restantes do MVP de desenvolvimento; o escopo READ-ONLY aceito foi preservado.

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
Dependência operacional histórica já satisfeita: P5 application DONE → P6.4 → P6.5 → P6.6;
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
