# Arquitetura

## Estilo

Modular-first, ports/adapters. Microserviços não são requisito.

## Núcleo

``` text
DiscordGateway
      ↓
Orchestrator/Application
 ├─ ProjectRegistry
 ├─ TaskRepository
 ├─ WorkflowEngine
 ├─ ApprovalPolicy
 ├─ AuditRepository
 ├─ PlannerPort ─────→ PlannerAdapter
 ├─ ExecutorPort ────→ CodexAdapter
 └─ LocalAgentPort ──→ LocalAgentAdapter
```

## Portas

### PlannerPort

Recebe contexto mínimo estruturado e retorna decisão estruturada:
investigar, preparar execução, concluir, bloquear ou pedir aprovação.

### ExecutorPort

Recebe uma `CODEX_TASK` fechada e retorna `CODEX_RESULT`.

### LocalAgentPort

Executa ação previamente validada pela política dentro do workspace do
projeto.

### TaskRepository

Persiste task, estado, dependências e evidências necessárias.

### ProjectRegistry

Mapeia `projectId` para workspace, políticas e configuração permitida.

### AuditRepository

Registra eventos relevantes com correlation IDs, sem segredos.

## Fonte de verdade

Mensagens Discord são UI/transporte. Estado de projeto/task não pode
depender de histórico de chat.

## Integrações externas

A forma concreta de integrar ChatGPT/OpenAI e Codex **não está definida
por suposição**. P3 e P4 começam verificando as interfaces oficiais
atuais e escolhendo adapters compatíveis.

## Invariantes

-   Executor não decide arquitetura.
-   Planner não executa shell diretamente.
-   Agente local não escolhe comandos por conta própria.
-   Toda execução pertence a projeto/task/correlation ID.
-   Uma task só chega ao Executor em estado `READY_FOR_CODEX`.
