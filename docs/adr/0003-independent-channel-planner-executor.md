# ADR 0003 — Independência de Channel, Planner e Executor

- Data: 2026-10-07
- Status: Accepted — decisão explícita do Planner.
- Escopo: restrição arquitetural permanente; alteração somente documental.

## Contexto

O Dev Orchestrator utiliza Ports & Adapters. As integrações escolhidas para
o MVP e os nomes históricos do protocolo não devem vincular a interface do
usuário aos fornecedores de planejamento ou execução. É necessário distinguir
três dimensões:

- **Channel**: onde o usuário interage com o Orchestrator.
- **Planner**: componente responsável pelo planejamento e pela decisão.
- **Executor**: componente responsável pela execução do trabalho autorizado.

MCP é protocolo de entrada; ChatGPT pode ser cliente desse adapter. Esse papel
de Channel é independente de qualquer implementação de Planner.

## Decisão

Channel, Planner e Executor são dimensões independentes e substituíveis.

Nenhum adapter de Channel pode determinar ou depender da implementação
concreta de Planner ou Executor. Nenhum Planner pode depender de um Channel
específico. Nenhum Executor pode depender de um Channel específico.

O domínio/application core não pode depender de ChatGPT, Discord, OpenAI,
Codex, Cursor ou qualquer outro fornecedor específico. Contratos internos e
ports devem permanecer independentes de SDKs, protocolos, credenciais e tipos
de fornecedores. Detalhes concretos pertencem aos adapters e à composição.

MCP/ChatGPT deve permanecer um adapter de entrada e nunca se tornar a
implementação ou autoridade do Planner. Uma integração de planejamento,
inclusive do mesmo fornecedor do Channel, deve ocupar separadamente o papel
de Planner atrás da respectiva port. A identidade do cliente não seleciona
implicitamente o Planner nem concede autoridade de decisão ou execução.

A seleção/configuração de Channel, Planner e Executor deve ocorrer fora do
domínio, através das respectivas ports/adapters e composition root. Trocar
uma dimensão não deve exigir trocar as outras. O Channel entrega intenção
e apresenta respostas por contratos de aplicação; a aplicação coordena os
papéis e preserva políticas, aprovações e limites existentes.

## Combinações ilustrativas

| Channel | Orquestrador | Planner | Executor |
| --- | --- | --- | --- |
| ChatGPT | Dev Orchestrator | OpenAI | Codex |
| Discord | Dev Orchestrator | OpenAI | Cursor |
| Discord | Dev Orchestrator | Local | Cursor |
| UI própria | Dev Orchestrator | Outro Planner | Outro Executor |

Esses exemplos expressam independência arquitetural; não aprovam nem
implementam integrações, não garantem disponibilidade de fornecedores e não
introduzem dependências específicas no domínio.

## Compatibilidade verificada

- [ADR 0001](0001-codex-executor-integration.md): mantém Planner atrás de
  PlannerPort e Executor atrás de ExecutorPort, com contratos independentes
  do provider. Codex app-server/OAuth permanecem escolha do adapter do MVP.
  As referências a ChatGPT/Planner e Codex/Executor descrevem os papéis e a
  configuração daquele contexto; não obrigam a mesma combinação para todo
  Channel. `CODEX_TASK`, `CODEX_RESULT` e `READY_FOR_CODEX` são nomes históricos
  de contratos/estado, não seletores de implementação. Nenhum contrato ou gate
  P4 é alterado; `ExecutorSession != TaskState` permanece vigente.
- [ADR 0002](0002-mcp-read-only-adapter.md): MCP é adapter de entrada sem
  autoridade própria, e consultas READ não disparam Planner ou Executor.
  O papel de ChatGPT como cliente MCP não o transforma no componente Planner.
  Identidade, autenticação, grants, autorização e auditoria foram implementados
  em MCP-3/MCP-4 e integrados localmente em MCP-5. A comprovação com Channel
  externo real permanece pendente de MCP-6; esta ADR não libera essa exposição.
- [Arquitetura](../ARCHITECTURE.md): Ports & Adapters permite seleção na
  composição; o diagrama com Discord/Codex representa uma configuração concreta.
- PlannerPort (`internal/ports/planner.go`, interface `ports.Planner`): usa
  requests/decisões internos, sem Channel ou SDK específico no contrato.
- ExecutorPort (`internal/ports/executor.go`, interface `ports.Executor`): usa
  requests/resultados independentes do provider, sem Channel no contrato.
- Discord (`internal/adapters/discord`): Gateway recebe serviços de query,
  continuação, aprovação e conversação por interfaces; não seleciona nem
  constrói um Planner ou Executor concreto. SDK e transporte ficam no adapter.
- [MCP-1/MCP-2](../tasks/MCP.md): DTOs READ independentes de transporte;
  fronteira MCP-2 privada na application utiliza repositórios e Local Agent,
  sem invocar Planner ou Executor e sem composição externa.

Não foi identificado conflito com as decisões aprovadas. Esta ADR explicita
a independência já prevista nas ports; não substitui as escolhas concretas
do MVP nem declara novos adapters implementados.

## Consequências e limites

Novas integrações devem preservar essa separação e os gates existentes.
Substituibilidade não concede permissões, não remove limites e não autoriza
fallback automático entre providers. Discord continua sem ser fonte de verdade.
Nenhum código, teste, adapter, composição, MCP-2 ou MCP-3 é alterado por esta
decisão. A revisão de MCP-2 continua separada da aprovação documental desta ADR.
