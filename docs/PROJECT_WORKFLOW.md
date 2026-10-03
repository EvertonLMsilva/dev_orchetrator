# Workflow

## Estados

``` text
PLANNED
  ↓
READY_FOR_ANALYSIS
  ↓
ANALYZING
  ├─→ BLOCKED
  ├─→ NEEDS_APPROVAL
  └─→ READY_FOR_CODEX
          ↓
      IN_PROGRESS
       ├─→ BLOCKED
       ├─→ FAILED
       └─→ DONE
```

`CANCELLED` pode encerrar qualquer estado permitido pela política.

## Ciclo

1.  selecionar menor task desbloqueada;
2.  Planner decide se possui evidência suficiente;
3.  se não, produz `BOT_COMMAND`;
4.  agente local executa e retorna `BOT_RESULT`;
5.  Planner refina/fecha task;
6.  `READY_FOR_CODEX`;
7.  Executor implementa/testa;
8.  retorna `CODEX_RESULT`;
9.  Planner revisa evidência;
10. DONE, correção atômica ou BLOCKED;
11. informar Discord.

## Escalonar ao humano

-   decisão de produto;
-   operação de risco;
-   custo acima do budget;
-   conflito arquitetural sem decisão registrada;
-   loop/retry excedido;
-   segredo/permissão necessária.

## Validação

Teste focado → integração próxima quando necessária → suíte ampla
somente em gate/mudança transversal.
