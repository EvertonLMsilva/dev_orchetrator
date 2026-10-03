# Dev Orchestrator --- AGENTS

## Princípio

ChatGPT/Planner **analisa e decide**. Codex/Executor **implementa uma
task fechada**. O agente local **executa somente ações permitidas**.

## Leitura mínima

Para implementar uma task, leia:

1.  `AGENTS.md`;
2.  a task indicada;
3.  somente os arquivos explicitamente permitidos pela task.

Não leia roadmap, histórico ou outros módulos por padrão.

## Execução

-   uma task atômica por vez;
-   TDD para domínio, estados, políticas, contratos, idempotência e
    protocolo;
-   mudança mínima;
-   sem refatoração lateral;
-   sem enfraquecer teste/contrato/política;
-   testes focados primeiro;
-   preserve alterações locais;
-   dúvida arquitetural bloqueante → `BLOCKED`, não investigação ampla.

## Segurança

-   nenhum comando destrutivo automático;
-   respeitar workspace e path policy;
-   comandos fora da allowlist exigem política/aprovação;
-   nunca expor segredos;
-   limites de tempo e saída obrigatórios;
-   Discord não é fonte de verdade.

## Economia de contexto

-   não repetir arquitetura já registrada;
-   não preparar próxima task;
-   não explorar "por garantia";
-   se a task exigir leitura ampla, devolver ao Planner para
    refinamento.

## Retorno padrão

``` text
STATUS: DONE | BLOCKED | FAILED
TASK:
ALTERADO:
TESTES:
EVIDÊNCIA:
OBSERVAÇÃO:
```
