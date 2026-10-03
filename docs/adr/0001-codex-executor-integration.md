# ADR 0001 — Integração oficial do Executor com OpenAI/Codex

- Data: 2026-10-03
- Status: Accepted for MVP behind ExecutorPort.
- Task: P4.1 — Official Codex Integration Discovery + ADR

## Contexto

O Dev Orchestrator é escrito em Go e recebe uma `CODEX_TASK` fechada para
produzir um `CODEX_RESULT`. Esta ADR registra a investigação e a decisão
arquitetural já realizadas e aprovadas pelo Planner. Não implementa a
integração nem refaz o discovery.

## Decisão

O primeiro provider real de `ExecutorPort` usará OpenAI Agents API,
SDK oficial Go da OpenAI, Codex harness, ambiente `self_hosted` e
`codex exec-server`. Linux/Docker será o ambiente oficial de execução.

```text
Dev Orchestrator (Go)
        │ ExecutorPort
        ▼
OpenAI/Codex Adapter
        │ official Go SDK
        ▼
OpenAI Agents API
        ▼
Codex Harness
        ▼
self_hosted environment
        ▼
codex exec-server
        ▼
controlled Linux/Docker workspace
```

O SDK oficial Go permite integração compatível com a linguagem do projeto
e evita adicionar Node/TypeScript apenas para usar o Codex TypeScript SDK.
A Agents API fornece uma abstração adequada para execução Codex, mantendo
os detalhes do provider fora do domínio. `self_hosted` e `codex exec-server`
mantêm o executor no ambiente Linux/Docker controlado pelo projeto.
A integração permanece substituível atrás de `ExecutorPort`.

## Isolamento obrigatório do provider

```text
domain/application
       │
       ▼
ExecutorPort
       │
       ▼
OpenAICodexAdapter
       │
       ▼
OpenAI Go SDK / Agents API
```

Tipos, IDs, eventos e estruturas específicos OpenAI/Codex não podem
atravessar o adapter para domain/application. Não criar abstrações do
domínio baseadas em response ID, thread ID, OpenAI session ID,
Codex-specific event types ou SDK-specific types. IDs do provider são
detalhes internos do adapter/infraestrutura.

## Sessões no MVP

```text
1 CODEX_TASK
     ↓
1 ExecutorSession
     ↓
1 Agents API Session
     ↓
execution
     ↓
CODEX_RESULT
     ↓
ExecutorSession CLOSED
```

Não reutilizar sessão entre tasks no MVP. `ExecutorSession != TaskState`.
A Agents API pode possuir sessões duráveis, mas essa capacidade não deve
virar requisito do domínio.

## Segurança

- Preservar fail closed.
- Não aceitar shell arbitrário vindo do Planner/modelo.
- Não permitir executable/argv livre no contrato de domínio.
- Executar em workspace controlado, com Linux/Docker como autoridade.
- A policy de execução pertence ao Executor; o provider não pode
  enfraquecê-la.
- Credenciais não pertencem ao domínio.
- Não expor API key principal ao workspace de execução quando a integração
  permitir credencial restrita apropriada.

## Superfície beta e consequência arquitetural

A Agents API utilizada atualmente possui superfície beta. A decisão é:
**Accepted for MVP behind ExecutorPort.**

A dependência beta não pode contaminar domain/application. Se a interface
oficial mudar, deve ser possível substituir o adapter preservando a porta
e os contratos independentes do provider.

## Alternativas consideradas

- **Codex TypeScript SDK:** preterido para evitar adicionar Node/TypeScript
  apenas para integrar um projeto Go.
- **Codex app-server:** preterido em favor da abstração de execução da
  Agents API com o SDK oficial Go.
- **`codex exec`/CLI direto:** preterido em favor da integração pela Agents
  API e da execução via `exec-server` no ambiente controlado.
- **Responses API + tool loop:** preterido em favor da abstração de execução
  Codex fornecida pela Agents API.
- **OpenAI-hosted execution environment:** preterido para manter a execução
  no ambiente Linux/Docker controlado pelo projeto via `self_hosted`.

## Fronteira P4/P5

> P4 sabe executar uma CODEX_TASK e produzir um CODEX_RESULT.
> P5 decide quando executar e como aplicar o resultado ao workflow/TaskState.

Esta decisão não inclui orquestração, alteração de workflow ou implementação
de contratos, adapter, SDK, Docker, `exec-server` ou credenciais.
