# ADR 0001 — Integração oficial do Executor com OpenAI/Codex

- Data: 2026-10-03
- Status: Accepted for MVP behind ExecutorPort; revisada por P4.1b, com gate de smoke test antes de P4.4.
- Task original: P4.1 — Official Codex Integration Discovery + ADR
- Amendment: P4.1b — autenticação/custo via conta ChatGPT.

## Contexto e motivação da revisão

O Dev Orchestrator é escrito em Go e recebe uma `CODEX_TASK` fechada para
produzir um `CODEX_RESULT`. Após esclarecer o requisito de autenticação/custo,
o Planner revisou a decisão: permitir o uso da conta e franquia do plano
ChatGPT do usuário quando oficialmente suportado, sem exigir `OPENAI_API_KEY`
e cobrança tradicional da API como caminho padrão do MVP.

ChatGPT permanece Planner; Codex permanece Executor; Dev Orchestrator faz a
coordenação. Esta revisão documenta a decisão, sem implementar integração.

## Histórico: decisão anterior de P4.1

O caminho originalmente aceito era:

```text
ExecutorPort
    ↓
Agents API
    ↓
self_hosted
    ↓
codex exec-server
    ↓
Codex
```

A decisão previa SDK oficial Go, Codex harness e execução Linux/Docker
controlada. Preferia a abstração da Agents API ao app-server, SDK TypeScript,
CLI direto e Responses API com tool loop; evitava ambiente OpenAI-hosted.
A superfície beta ficava isolada atrás de `ExecutorPort`.

Esse caminho depende de credenciais/API billing e deixa de ser o caminho
principal do MVP. O histórico acima registra a decisão anterior, não uma
exigência para o novo adapter. Integração baseada em API key poderá existir
futuramente como outro provider/adapter, mas não é requisito do MVP.

## Decisão revisada

A autenticação preferencial será **Sign in with ChatGPT OAuth**, com
**Codex app-server**, em vez de `OPENAI_API_KEY`:

```text
CODEX_TASK
    ↓
ExecutorPort
    ↓
ExecutionPackage
    ↓
CodexProviderAdapter
    ↓
codex app-server
    ↓
ChatGPT OAuth access token
    ↓
Codex
    ↓
normalized result
    ↓
ExecutorResult
```

O token no diagrama representa autenticação de infraestrutura, não conteúdo
do pacote ou da task. O adapter traduz o pacote e normaliza o resultado.

### Base oficial e limites

A documentação oficial descreve o uso de um access token OAuth autorizado
para o plano ChatGPT com um provider Responses no app-server, sem sign-in
Codex separado. Um turno de inferência concluído verifica acesso ao modelo;
`model/list` sozinho não comprova esse acesso.
Fonte: [Codex app-server — Sign in with ChatGPT](https://developers.openai.com/siwc/token-sharing-open-source/codex-app-server).

O uso do plano é uma capacidade opcional, sujeita a consentimento e permissões
da conta/workspace para requisições elegíveis; não dá acesso às conversas
ChatGPT. Não se presume disponibilidade universal nem franquia ilimitada.
Fonte: [ChatGPT plan usage — Overview](https://developers.openai.com/siwc/token-sharing-open-source).

A configuração documentada usa stdio local e HTTP/SSE para Responses; a
renovação do token é responsabilidade da aplicação. Há limitações de preview
que deverão ser respeitadas pelo adapter e verificadas no gate.
Fonte: [Preview limitations](https://developers.openai.com/siwc/token-sharing-open-source/preview-limitations).

## Fronteiras e contratos preservados

O Planner continua atrás de `PlannerPort`. Domínio/application não dependem
de detalhes OpenAI/OAuth. O Executor continua atrás de `ExecutorPort`.
Codex app-server, OAuth, access tokens, IDs específicos do provider, eventos
OpenAI e detalhes do protocolo pertencem somente ao adapter/provider.

A revisão **não invalida** os contratos de P4.2/P4.3: `ExecutorPort`,
`ExecutorRequest`, `ExecutorResult`, `ExecutorTaskSpec`, `ExecutionPackage`
e `WorkspaceSandbox`, nem validação de Scope, resolução segura de Workspace
ou política fail-closed. Todos permanecem provider-independent.

OAuth tokens são infraestrutura e segredos: não pertencem ao domínio, não
entram em `CODEX_TASK` ou `ExecutionPackage`, não são persistidos como estado
canônico do projeto e não aparecem em logs. Armazenamento e renovação seguros
são responsabilidade da infraestrutura.

## Execução local e segurança

Direção arquitetural, sem definir ainda o mecanismo final de sandbox/runtime
reservado a P4.4/P4.6:

```text
Dev Orchestrator
       ↓
Codex Adapter
       ↓
codex app-server
       ↓
controlled Linux/Docker environment
       ↓
Project Workspace
```

- Workspace controlado e scope fechado.
- Linux/Docker como autoridade de validação.
- Sem arbitrary shell fornecido pelo Planner/modelo; sem executable/argv
  livre no contrato de domínio.
- Sem Docker socket exposto desnecessariamente e sem secrets no workspace.
- Fail closed; o provider não pode enfraquecer a policy do Executor.

## Sessões no MVP

`ExecutorSession != TaskState`.

```text
1 CODEX_TASK
    ↓
1 ExecutorSession
    ↓
Codex execution
    ↓
1 CODEX_RESULT
    ↓
session closed
```

Não reutilizar sessão entre tasks no MVP. Sessões do provider não se tornam
estado canônico do domínio. Qualquer thread/session ID específico do Codex
permanece dentro do adapter; capacidade de retomada não muda essa regra.

## Fronteira P4/P5 e arquitetura futura

> P4 sabe executar uma CODEX_TASK e produzir um CODEX_RESULT.
> P5 decide quando executar e como aplicar o resultado ao workflow/TaskState.

O ciclo conceitual completo pertence ao P5+; P4 responde apenas pelo Executor:

```text
User
 ↓
Discord
 ↓
Dev Orchestrator
 ↓
Planner
 ↓
CODEX_TASK
 ↓
Executor
 ↓
Codex
 ↓
CODEX_RESULT
 ↓
Planner
 ↓
Discord
```

No P5, cada canal Discord poderá ser associado a um projeto por
CLI/configuração persistente:

```text
GuildID + ChannelID
        ↓
ProjectID
        ↓
Project.Workspace

#dev-orchestrator → dev-orchestrator
#mktauto          → mktauto
#market-direction → market-direction
```

Workspace nunca é fornecido livremente por Discord/Planner; o sistema o
resolve a partir de `ProjectID`. Esta tarefa não implementa esse mapeamento,
Discord, workflow, OAuth, app-server ou mudanças nos contratos existentes.

## Próximo gate: smoke test descartável

Antes da implementação definitiva de P4.4, comprovar:

```text
ChatGPT authentication
        ↓
OAuth access token
        ↓
codex app-server
        ↓
minimal Codex execution
        ↓
result returned
```

Requisitos: sem `OPENAI_API_KEY`, fora do código de produção, sem commit de
código experimental, sem workspace real do projeto inicialmente, sem Docker
socket, sem exposição de secrets e com tarefa mínima descartável.
Somente após esse teste passar implementar P4.4. O smoke test não é executado
em P4.1b; disponibilidade/autorização e execução efetiva ainda devem ser
comprovadas nesse gate.
