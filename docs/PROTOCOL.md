# Protocolo

Todo envelope possui:

``` text
protocolVersion
messageType
messageId
correlationId
projectId
taskId?
createdAt
payload
```

## Tipos

### BOT_COMMAND

Planner solicita uma ação local permitida.

Payload implementado in-process: `application.BotCommand{Action: domain.Action}`.

A Action usa a união fechada de parâmetros existente, sem comandos, shell ou argv.
Envelope e Action são validados antes da execução; ProjectID e TaskID devem coincidir.
Payloads de outros tipos são rejeitados. Não há serialização JSON nesta fronteira.

### BOT_RESULT

Payload concreto `application.BotResult`: ActionType, Status, Result tipado opcional
e Error com código fixo opcional. Status: SUCCESS, BLOCKED, APPROVAL_REQUIRED ou FAILED.
Somente SUCCESS contém ActionResult validado e correspondente à ação. Erros não
incluem texto bruto de dependências ou resultados parciais. Cancellation/deadline
geram FAILED com CANCELED/DEADLINE_EXCEEDED; o contexto original chega ao LocalAgent.
CorrelationID, ProjectID e TaskID são preservados; MessageID recebe sufixo :result.
Entrada inválida retorna erro fixo antes do dispatcher, sem BOT_RESULT.
APPROVAL_REQUIRED informa a decisão sem executar ou iniciar workflow.

### PLANNER_DECISION

``` json
{"decision":"INVESTIGATE|READY_FOR_CODEX|DONE|BLOCKED|NEEDS_APPROVAL","reason":"..."}
```

### CODEX_TASK

Contém referência da task fechada, escopo e restrições. Não contém
"descubra o que fazer".

### CODEX_RESULT

``` json
{"status":"DONE|BLOCKED|FAILED","changedFiles":[],"tests":[],"evidence":[]}
```

### APPROVAL_REQUEST / APPROVAL_RESULT

Usados quando uma política exige decisão humana.

## Regras

-   respostas devem correlacionar com `correlationId`;
-   resultados atrasados de uma tentativa anterior não podem alterar
    estado atual;
-   payloads são validados por schema;
-   output bruto grande deve ser limitado/referenciado, não replicado
    indefinidamente;
-   mensagens Discord não substituem persistência.
