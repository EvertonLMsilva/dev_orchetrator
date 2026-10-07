# ADR 0002 — MCP como adapter de entrada READ-ONLY

- Data: 2026-10-07
- Status: decisão arquitetural registrada; implementação condicionada aos gates abaixo.
- Task: MCP-0 — formalização documental da integração MCP READ-ONLY.

## Contexto

Queremos permitir que ChatGPT e outros clientes consultem o Dev Orchestrator
por MCP, preservando Ports & Adapters e as fronteiras de segurança existentes.
ChatGPT permanece Planner; Codex permanece Executor de uma task fechada.

O experimento externo informado pelo Planner comprovou o caminho cliente →
control plane → tunnel → servidor MCP fake → resposta. Essa evidência de
transporte não comprova integração real, identidade, autorização ou auditoria
do Dev Orchestrator e não libera implementação por si só.

## Decisão

MCP será um adapter de entrada, com localização prevista em
`internal/adapters/mcp`. Protocolo, SDK e transporte ficam no adapter e na
composição; domínio, ports e use cases permanecem independentes de MCP e de
qualquer cliente específico.

A primeira fase será exclusivamente READ-ONLY. O adapter chamará use cases
de consulta da application, usando os ports e gates internos existentes.
Não acessará filesystem, Git ou Docker diretamente e não chamará Executor
fora dos use cases/ports autorizados. MCP não possui autoridade própria sobre
Git, Executor, Docker, políticas ou aprovações e não cria bypass nem uma
segunda autoridade de autorização.

Fluxo previsto:

```text
Cliente MCP → fronteira autenticada → adapter MCP
  → consulta autorizada/auditada na application
  → ports e gates existentes → resposta segura
```

Consultas não criam tasks, não transitam workflow e não disparam Planner ou
Executor. Escrita operacional de auditoria não concede escrita no projeto.
Workspace é resolvido pelo registro confiável a partir do projeto autorizado,
nunca recebido livremente do cliente. Allowlist, sandbox, isolamento, limites,
cancelamento e fail closed permanecem obrigatórios.

## Ferramentas previstas

| Ferramenta | Responsabilidade e limite |
| --- | --- |
| `project.status` | Consultar informações e disponibilidade de consultas do projeto; não inventar um estado de saúde ausente do domínio. |
| `project.tasks` | Listar tasks pertencentes ao projeto autorizado, com projeção segura e limites. |
| `git.status` | Consultar Git pela fronteira controlada do Local Agent, preservando policy, allowlist, resolução do projeto e gates READ existentes. |
| `execution.status` | Consultar observação de execução somente quando existir fonte confiável própria; TaskStatus não comprova execução. |

`ExecutorSession != TaskState` continua vigente. `execution.status` não pode
inferir início, ausência, término ou resultado de execução a partir de
TaskStatus. Sem fonte consultável de observação, deve declarar indisponibilidade;
TaskStatus, se retornado, será identificado separadamente. Histórico de sessões,
persistência e contratos detalhados não são implementados por MCP-0.

## Fronteiras futuras

`READ → PLAN → WRITE → EXECUTE` representa a separação de capacidades para
evolução futura, não uma promoção automática de permissões nem uma sequência
de chamadas obrigatória. Cada classe exigirá decisão, contrato e autorização
próprios antes de implementação. Apenas READ integra a primeira fase;
PLAN, WRITE e EXECUTE permanecem fora da superfície MCP inicial.

## Gates antes da implementação

O Planner deve resolver a identidade confiável fornecida pela fronteira de
transporte, o mecanismo de autenticação e a autorização por principal,
operação e projeto antes da implementação. Tunnel, conexão, SessionID MCP,
metadados do cliente e argumentos do modelo não constituem autoridade.
Autenticação de acesso MCP não reutiliza implicitamente credenciais do Executor.

A autorização de consultas pertence à application, com uma fonte confiável
de grants; o adapter apenas entrega a identidade validada. Classificação AUTO
de uma ação não autoriza acesso de um principal a um projeto. Ausência de
identidade ou concessão válida deve negar acesso.

Também devem ser definidos auditoria independente de Discord, projeções sem
segredos, limites de entrada/saída e compatibilidade da versão do protocolo/SDK.
Auditoria deve registrar correlação, principal, operação, decisão e resultado
sem tokens ou conteúdo bruto desnecessário; sua falha deve seguir política
fail closed. Estes gates não estão declarados resolvidos nesta ADR.

## Compatibilidade e escopo

### Decisão do Planner — MCP-3 (2026-10-07)

Identidade será produzida por `ports.AuthenticationPort`, independente de
fornecedor, verificando material de autenticação entregue pelo futuro adapter.
Principal fornecido como string livre pelo Channel não é aceito. Channel não
é Principal; credenciais de Planner/Executor não autenticam implicitamente
o usuário. Nenhum OAuth ou mecanismo específico é escolhido por MCP-3.

Grants serão consultados por requisição através de `ports.GrantRepository`,
sem configuração estática como autoridade definitiva na application. Cada
grant concede exatamente uma operação READ a um Principal em um Project.
Ausência, erro ou divergência de qualquer campo nega acesso. Não há roles,
grupos, curingas, herança, expansão, concessões implícitas ou promoção por AUTO.

Fluxo aprovado: autenticação → autorização do grant exato → resolução do
projeto no registro confiável → MCP-2. Qualquer falha anterior impede a chamada
ao MCP-2. Application não depende de Channel, Planner ou Executor concretos.
As implementações de autenticação/persistência definitiva ficam fora desta
entrega. Auditoria fail-closed permanece gate obrigatório separado antes da
exposição externa; MCP-3 não implementa servidor, transporte, WRITE ou MCP-4.

- [ADR 0001](0001-codex-executor-integration.md): preserva ExecutorPort,
  autenticação do provider, isolamento e contratos provider-independent.
  MCP não substitui a integração do Executor nem resolve B-P4-001.
- [P4](../tasks/P4.md): preserva CODEX_TASK → Executor → CODEX_RESULT e a
  fronteira P4/P5; consultas MCP não iniciam execução nem alteram lifecycle.
- [P10](../tasks/P10.md): preserva candidato, aprovação contextual, mediated
  applier e gates Git independentes. READ não concede WRITE_APPLY ou operações
  Git mutáveis. MCP-0 não resolve P10.4, não define bootstrap/layout/ownership
  de metadata e não enfraquece a rejeição de `.git` em ManagedWorkspace.
  `git.status` fica limitado a projetos compatíveis com os gates READ existentes.

Esta entrega altera somente documentação. Não implementa código, handlers,
schemas executáveis, dependências ou composição MCP; não altera Executor,
P10, Git ou Docker e não inicia MCP-1 ou qualquer task posterior.

## MCP-4 — Secure READ Runtime (2026-10-07)

O Planner aprovou a entrega interna de adapters locais configuráveis para
autenticação/grants, auditoria obrigatória fail-closed e composição segura.
A implementação inicial verifica evidência opaca contra hashes SHA-256 de
tokens aleatórios administrados localmente, usando comparação constante de
digests. Autenticação e grants são relidos por requisição. Configuração fica
na infraestrutura, fora da política de application. Nenhum fornecedor, OAuth,
Channel, Planner ou Executor concede identidade/permissões implicitamente.

Auditoria possui port própria independente de Discord. Um registro RECEIVED
deve ser persistido antes da autenticação e AUTHORIZED antes do READ. Denials
e resultados possuem eventos próprios, sem evidence/tokens/erros brutos.
Falha de auditoria impede continuar ou entregar o resultado. Falha no registro
final não desfaz uma leitura já feita; não há mutações de projeto. A tentativa
fica identificada por AttemptID gerado internamente e CorrelationID validado.
Detalhes de configuração, operação e limitações em [MCP](../tasks/MCP.md).

MCP-4 não libera exposição externa. MCP-5 tratará Gateway/transporte/runtime/
Docker/health/readiness; MCP-6 comprovará READ E2E externo positivo e negativo.

## Decisão adicional do Planner — MCP-5 (2026-10-07)

MCP será inbound adapter do próprio Dev Orchestrator, hospedado pelo Service,
composition root e entrypoint existentes. Não haverá MCP Gateway independente.
Authentication, Authorization, Grants, Audit e Application permanecem no mesmo
processo; Docker é deployment/isolation, não fronteira de domínio. Isso substitui
as referências anteriores a um Gateway separado. Executor containers existentes
permanecem responsabilidade própria e não são redesenhados por MCP-5.

A entrega usa SDK oficial Go e Streamable HTTP local, evidence por Bearer
encaminhada ao MCP-4 e somente as quatro operações READ aprovadas. O adapter não
autentica/autoriza por conta própria, não consulta repositories diretamente e
não seleciona providers. A configuração pode ativar MCP junto a Discord ou
desativar explicitamente Discord; READ puro não instancia Planner/Executor.
ChatGPT real, tunnel e OAuth permanecem fora de MCP-5, reservados a MCP-6
quando necessários. Configuração operacional e limitações em [MCP](../tasks/MCP.md).
