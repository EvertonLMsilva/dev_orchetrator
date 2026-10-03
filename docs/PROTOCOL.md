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

Payload conceitual:

``` json
{"commandId":"...","action":"search","args":{},"reason":"..."}
```

### BOT_RESULT

``` json
{"commandId":"...","status":"SUCCESS|FAILED|DENIED","exitCode":0,"output":"...","truncated":false}
```

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
