# Visão do Projeto

## Problema

O desenvolvimento assistido por IA ainda exige trabalho manual para
copiar comandos, resultados, contexto e prompts entre ChatGPT, terminal,
Codex e Git. Isso aumenta tempo, erro humano e consumo de tokens.

## Objetivo

Criar um orquestrador que permita conduzir o ciclo de desenvolvimento
pelo Discord, mantendo responsabilidades separadas:

-   humano: objetivos, aprovações e decisões de negócio;
-   Planner: análise, arquitetura, diagnóstico e especificação;
-   agente local: evidência e comandos controlados;
-   Executor: alteração concreta e testes;
-   Orchestrator: estado, políticas, roteamento e auditoria.

## Fluxo alvo

``` text
Humano/Discord
      ↓
Orchestrator
      ↓
Planner
  ↙       ↘
Local     Executor
Agent      Codex
  ↘       ↙
 Orchestrator
      ↓
Discord
```

## MVP

P0--P5. Deve suportar um projeto piloto, tasks atômicas, investigação
local controlada, planejamento, execução Codex e review até
DONE/BLOCKED.

## Não objetivos do MVP

-   autonomia irrestrita;
-   execução arbitrária de shell;
-   microserviços;
-   dashboard complexo;
-   merge automático;
-   múltiplos planners/executors;
-   substituir Git ou o repositório como registro durável.

## Princípios

TDD, menor privilégio, contratos pequenos, estado persistido, adapters
substituíveis, auditabilidade, aprovação proporcional ao risco e
economia extrema de contexto.
