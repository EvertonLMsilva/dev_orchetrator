# Arquitetura

## Estilo

Modular-first, ports/adapters. Microserviços não são requisito.

Channel (interação do usuário), Planner (planejamento/decisão) e Executor
(execução) são dimensões independentes e substituíveis. Seleção e configuração
ocorrem fora do domínio, nas ports/adapters e composition root; nenhum Channel
determina implementações concretas de Planner/Executor. O core permanece
independente de fornecedores. Restrição permanente e compatibilidade:
[ADR 0003](adr/0003-independent-channel-planner-executor.md).

## Núcleo

O diagrama abaixo ilustra uma configuração concreta; Discord e Codex não
constituem dependências obrigatórias nem vinculam as três dimensões.

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

MCP é inbound adapter do mesmo processo Dev Orchestrator, conforme decisão
do Planner em MCP-5. Authentication, Grants, Authorization, Audit e READ são
fronteiras de código in-process; não são serviços/processos/containers separados.
O Service/composition root e `cmd/orchestrator` existentes hospedam o listener
MCP e seu lifecycle. Docker implementa deployment/isolation. Um deployment
somente MCP READ não inicializa Planner/Executor nem requer suas credenciais;
a seleção deles permanece independente do Channel. Configuração e limites:
[MCP](tasks/MCP.md).

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

## Local Agent implementado (P2.9–P2.10)

`LocalAgentTransport` representa a fronteira de protocolo in-process, sem
IPC, JSON ou rede. Aceita somente `BOT_COMMAND` com `BotCommand` concreto,
valida Envelope/Action e exige igualdade de ProjectID e TaskID (inclusive
presença). Chama exclusivamente `ports.LocalAgent.Execute`.

O dispatcher mantém Policy, Allowlist e consulta ao ProjectRepository antes
das cinco capabilities existentes. Sandbox e limites permanecem nos
executores. O transport não concede permissões nem cria approvals.
