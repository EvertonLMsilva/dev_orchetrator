# TASK_SPEC

``` markdown
# <ID> — <nome>

STATUS:
PRIORIDADE:
DEPENDE_DE:
PROJECT_ID:

## Objetivo e valor

## Evidências/decisões já conhecidas

## Context budget
- arquivos máximos esperados:
- output máximo:
- investigação permitida:

## Arquivos
READ:
CREATE:
MODIFY:

## Contrato
Entrada:
Saída:
Estados/erros:
Invariantes:

## Implementação
1.
2.

## Testes
1.
2.

## Aceite

## NÃO FAZER

## Política
AUTO:
APPROVAL:
BLOCKED:

## Retorno esperado
STATUS:
ALTERADO:
TESTES:
EVIDÊNCIA:
OBSERVAÇÃO:
```

## Atomicidade

Se o Executor precisar escolher arquitetura, descobrir escopo, ler
muitos módulos ou implementar duas capacidades independentes, a task
deve ser dividida/refinada pelo Planner.
