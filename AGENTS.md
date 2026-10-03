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

## Validação Go

-   Ambiente oficial de validação: Linux via Docker.
-   Não considerar bloqueio do Windows Smart App Control como falha do
    código sem evidência adicional.
-   Para tasks Go, executar ao final `./scripts/validate.sh` dentro do
    container Linux; o aceite final deve usar esse ambiente.
-   Não desabilitar Smart App Control nem criar exceções de segurança
    para executar testes.
-   Testes focados durante TDD podem rodar normalmente no ambiente
    disponível.

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
