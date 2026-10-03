# Modelo de Erros

Erros normalizados iniciais:

-   `POLICY_DENIED`
-   `APPROVAL_REQUIRED`
-   `TIMEOUT`
-   `OUTPUT_LIMIT`
-   `INVALID_PROTOCOL`
-   `INVALID_STATE`
-   `PROJECT_NOT_FOUND`
-   `TASK_NOT_FOUND`
-   `TASK_NOT_READY`
-   `PATH_DENIED`
-   `LOCAL_AGENT_FAILED`
-   `PLANNER_FAILED`
-   `EXECUTOR_FAILED`
-   `TEST_FAILED`
-   `RETRY_LIMIT`
-   `DEPENDENCY_BLOCKED`

## Retry

Retry automático somente para falhas explicitamente classificadas como
transitórias e idempotentes.

Não repetir automaticamente: - falha de teste determinística; - policy
denied; - invalid protocol/state; - decisão arquitetural ausente.

## Loop prevention

Cada task/correlation mantém contador de tentativas e fingerprint do
erro. Repetição equivalente acima do limite → BLOCKED e humano/Planner.
