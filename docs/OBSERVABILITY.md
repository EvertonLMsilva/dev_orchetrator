# Observabilidade

## Audit mínimo

Registrar: - projectId/taskId; - messageId/correlationId; -
ator/componente; - transição de estado; - ação/política; - duração; -
resultado; - tentativas; - consumo/custo quando a integração fornecer
dado confiável.

## Não registrar

-   API keys/tokens;
-   variáveis secretas;
-   conteúdo privado desnecessário;
-   outputs integrais quando um resumo/hash/referência basta.

## Métricas úteis

-   tasks DONE/BLOCKED/FAILED;
-   tempo por estado;
-   comandos locais por task;
-   retries;
-   testes;
-   tokens/custo por Planner/Executor quando disponíveis;
-   quantidade de contexto/arquivos enviados ao Executor.

Não criar observabilidade complexa antes de existir fluxo real.
