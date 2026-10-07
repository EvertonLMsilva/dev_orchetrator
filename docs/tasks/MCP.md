# MCP — READ Application Boundary

## MCP-7 — Production MCP Runtime

Estado: DONE — aprovado pelo Planner; MCP7_IMPLEMENTATION=IMPLEMENTATION_COMPLETE;
LOCAL_VALIDATION=LOCAL_VALIDATION_COMPLETE; EXTERNAL_E2E=PASS.
MCP-8 permanece NEXT, não iniciado.

Aceite operacional externo aprovado pelo Planner em 2026-10-07: startup oficial
via `scripts/mcp-runtime.ps1`, build runtime, `/readyz` e OpenAI Secure MCP Tunnel
em foreground PASS. E2E real ChatGPT → Tunnel → MCP:

| Operação | Resultado aprovado | CorrelationID |
| --- | --- | --- |
| project.status | PASS | mcp7-final-status-003 |
| project.tasks | PASS; tasks=[]; hasMore=false | mcp7-final-tasks-003 |
| git.status | PASS; changedEntries=242 | mcp7-final-git-003 |
| execution.status | PASS; executionObservation=UNAVAILABLE | mcp7-final-exec-003 |

Durable audit confirmado no volume `dev-orchestrator-mcp-state`: as quatro
correlações atuais têm RECEIVED → AUTHORIZED → SUCCESS; eventos anteriores,
incluindo `mcp7-external-status-002`, sobreviveram ao restart. Projeto
`mcp7-unauthorized-project`, correlação `mcp7-final-deny-003`: RECEIVED →
AUTHORIZATION_DENIED, sem AUTHORIZED ou READ posterior. Após Ctrl+C, o container
`dev-orchestrator-mcp-orchestrator-1` terminou com `Exited (0)`.

Correção de git.status mergeada no PR #11, commit
`ad47c9a2d6389d92a595d23189b3dc198c91d531`, e comprovada no E2E externo acima.

Operação oficial no Windows: `./scripts/mcp-runtime.ps1`. Fornecer externamente
CONTROL_PLANE_API_KEY, MCP_CLIENT_TOKEN e CONTROL_PLANE_TUNNEL_ID. Defaults:
config `.runtime/mcp-real/orchestrator.json`, workspace do repositório, porta
127.0.0.1:8080 e executável `tunnel-client/tunnel-client.exe` no host.
ConfigFile, Workspace, TunnelExecutable e LocalPort podem ser explícitos.
Na primeira instalação, usar `-SecuritySource <diretório administrativo>` com
auth.json/grants.json; fonte Windows é montada somente no helper de provisionamento,
nunca no runtime. Instalação em volume Linux usa root/root 0600, arquivo regular
e link count 1. Execuções seguintes podem omitir SecuritySource; se fornecida,
deve coincidir com o volume existente. Material divergente, parcial ou permissões
inválidas bloqueiam; não há reparo/rotação automática.

Compose usa volumes externos `dev-orchestrator-mcp-security` (read-only no
runtime) e `dev-orchestrator-mcp-state`. Startup cria somente volumes ausentes,
valida security, prepara StateDir 0700, faz build target runtime e espera /readyz.
Tunnel executa em foreground após readiness, com referências de ambiente para
credenciais e logging raw desabilitado; saída é suprimida para proteger secrets.
Ctrl+C/saída do Tunnel encerra a operação e para somente seu serviço Compose;
falha após início também tenta stop. Volumes e container parado são preservados
para restart. Runtime já ativo/porta ocupada bloqueiam antes de alterar volumes.
Não iniciar em paralelo; falha de stop exige verificar o serviço antes de repetir.

Validação Linux/Docker em 2026-10-07: RED/GREEN do provisionamento e startup;
`scripts/test-mcp-security.sh` PASS (idempotência, permissões, hardlinks, symlink,
material ausente/divergente); `scripts/test-mcp-runtime.ps1` PASS (Compose);
`scripts/test-mcp-startup.ps1` PASS (pré-requisitos, lifecycle/rollback com mocks,
sem Tunnel real); `timeout 300 sh ./scripts/validate.sh` PASS (test/vet/build).
Build runtime PASS. Compose isolado em 127.0.0.1:18087: /readyz 200, cliente SDK
local com quatro READs PASS e unauthorized DENY_AUDITED. Stop/force-recreate ficou
healthy; prefixo integral da auditoria anterior preservado (novos eventos de
shutdown podem ser acrescentados). Volumes locais mantidos e runtime de teste
parado. Runtime real preexistente preservado. Sem commit, Tunnel real ou E2E ChatGPT.

## MCP-6 — External READ E2E Completion

Estado: DONE — aceite externo aprovado pelo Planner; IMPLEMENTATION_COMPLETE;
LOCAL_VALIDATION_COMPLETE; EXTERNAL_E2E=PASS.
Baseline externo informado pelo Planner: project.status e project.tasks funcionam;
storage vazio retorna []; execution.status retorna UNAVAILABLE; git.status era
negado pelo gate de configuração, após AUTHORIZED, com READ_FAILURE.

O gate READ permite explicitamente core.symlinks (incluindo false): controla
a representação de symlinks no checkout, sem comando, include ou carregamento
de código. branch.<nome>.vscode-merge-base é metadata opaca do editor; os
executores Git READ não a interpretam como comando ou caminho externo. Somente
essa chave específica foi adicionada, sem permitir outras chaves vscode/branch.
Filters, includes, fsmonitor, attributesFile e extensions.worktreeConfig
continuam negados; a combinação com metadata legítima também permanece negada.

project.tasks consulta o TaskRepository operacional: StateDir/tasks.json é
carregado em memória no startup. Ausência do arquivo é storage válido e vazio,
com tasks=[] e hasMore=false. Markdown, roadmap e Discord não são importados.
Testes READ usam o adapter persistente real para vazio, task de dev-orchestrator,
filtro por ProjectID, ordenação por TaskID, paginação e reabertura do snapshot.

AVAILABLE significa capacidade configurada/exposta, não health check nem sucesso
da última chamada. project.status não executa Git e continua AVAILABLE mesmo
após falha de git.status. execution.status continua UNAVAILABLE.

Aceite externo real aprovado pelo Planner: project.status PASS; project.tasks
PASS com tasks=[] e hasMore=false (vazio operacional legítimo); git.status PASS
no workspace real com changedEntries=136; execution.status PASS com UNAVAILABLE
esperado; projeto não autorizado PASS / AUTHORIZATION_DENIED; durable audit PASS.

Fluxo comprovado: ChatGPT → OpenAI Secure MCP Tunnel → Bearer evidence →
AuthenticationPort → Principal chatgpt-reader → grant exato
Principal+Operation+Project → durable audit → MCP READ boundary → workspace real
→ resposta ao ChatGPT.

Correlações de auditoria aprovadas pelo Planner:
- git.status, mcp6-git-redeploy-002: RECEIVED → AUTHORIZED → SUCCESS.
- project.tasks, mcp6-tasks-accept-003: RECEIVED → AUTHORIZED → SUCCESS.
- projeto não autorizado, mcp6-final-deny-001: RECEIVED → AUTHORIZATION_DENIED,
  sem AUTHORIZED ou execução posterior.

Nenhum OAuth, exposição pública, mudança de AuthenticationPort ou bypass foi
introduzido. Não houve commit nem início de MCP-7/MCP-8.

Validação Linux/Docker em 2026-10-07, imagem dev-orchestrator-mcp5-final-validation,
sem rede e com .runtime ocultado por tmpfs: RED do teste de metadata legítima
reproduziu read-only execution denied; GREEN dos testes focados TestReadOnlyGit
e TestReadBoundary; timeout 300 sh ./scripts/validate.sh PASS (exit 0:
go test ./..., go vet ./..., go build ./...). Nenhuma concorrência nova.
git diff --check PASS. Estes resultados não são prova de E2E externo.

MCP-1 — READ contracts/validation — DONE, aprovado pelo Planner.
Contratos existentes em `internal/application/readcontracts` preservados.

## MCP-2 — escopo aprovado pelo Planner

Criar uma camada interna de aplicação entre os requests validados do MCP-1 e
as capacidades READ existentes: dispatch somente das operações aprovadas,
invocação das capacidades e conversão ao contrato correspondente. Operação,
request, dependência ou resultado inválido falham fechado, sem resposta parcial.
TDD obrigatório; nenhuma nova arquitetura para capacidades ausentes.

| Operação | Capacidade utilizada | Projeção |
| --- | --- | --- |
| `project.status` | `ProjectRepository.FindByID` | Nome e disponibilidade configurada das consultas; não indica saúde, autorização nem garante sucesso do Git. |
| `project.tasks` | `ProjectRepository.FindByID` e `TaskRepository.FindByProject` | Somente tasks do projeto, ordenadas por TaskID, paginação e `HasMore`. |
| `git.status` | `LocalAgentDispatcher.Execute` com `GIT_STATUS` | Somente contagem de entradas; policy, allowlist, registro de workspace e gates Git existentes preservados. |
| `execution.status` | Registro do projeto; nenhuma fonte confiável consultável de execução disponível | `ExecutionObservation=UNAVAILABLE`; não consulta nem infere execução de TaskStatus. |

Implementação privada em `internal/application/read_boundary.go`, sem símbolos
exportados ou ligação com entrypoints. Não concede autoridade de autorização.
Dependências são composição trusted interna; argumentos não fornecem workspace,
commands, credenciais ou permissões. O Git usa o Local Agent existente, sem
acesso direto da nova camada a filesystem, Git ou Docker.

Requests são revalidados defensivamente; respostas usam validação MCP-1 e teto
de JSON. Erros de backend são convertidos em erro genérico, sem conteúdo bruto.
Tasks inconsistentes, duplicadas ou de outro projeto invalidam toda a consulta.
Paginação não modifica a coleção do repositório. Disponibilidade representa
capacidade configurada, não health check nem decisão de acesso.

Fora de escopo: servidor MCP, stdio/HTTP/SSE, autenticação, descoberta de
identidade, grants, autorização por principal/operação/projeto, auditoria MCP,
WRITE, MCP-3, alterações arquiteturais P4/P10 e abstrações especulativas.
Esses gates da ADR 0002 permanecem pendentes antes de exposição externa.
ADR 0001 e `ExecutorSession != TaskState` preservados; nenhum Executor,
workflow, approval, mediated applier ou Git mutável é invocado.

## Evidência de implementação

RED: teste focado falhou por ausência de `newReadBoundary`/`errReadQuery`.
RED adicional: resposta com títulos escapados excedendo o teto JSON foi aceita;
corrigido com verificação do tamanho serializado, sem alterar MCP-1.
Testes cobrem operações, correlação, projeções, paginação, cancelamento,
dependências ausentes, falhas de repositório, identidade divergente, tasks
inválidas/duplicadas/de outro projeto e Git negado pela allowlist.
Estado: implementado, aguardando review do Planner; sem commit ou execução LIVE.

GREEN final em Linux/Docker, imagem `dev-orchestrator-p6-5-validation`, rede
desabilitada e `.runtime` ocultado com tmpfs:

- `go test ./internal/application ./internal/application/readcontracts -count=1 -timeout=120s`: PASS.
- `go vet ./internal/application ./internal/application/readcontracts`: PASS.
- `git diff --check`, `git diff --cached --check` e verificação `--no-index --check`
  dos três novos arquivos: sem erros de whitespace (o diff no-index retorna 1
  por haver arquivos novos).

Somente testes relevantes e validação estática, conforme autorização MCP-2;
sem pipeline real, transporte, auth, auditoria, WRITE ou MCP-3.

Limite concreto: `FindByProject` retorna a coleção completa antes da paginação;
MCP-2 limita a resposta, mas não altera esse contrato de armazenamento existente.

## MCP-3 — Identity & Authorization Boundary

MCP-2 e MCP-3 foram aprovados pelo Planner; sem commit nesta sessão.
Decisões aprovadas registradas na ADR 0002: AuthenticationPort independente de
fornecedor verifica evidência opaca e produz Principal; GrantRepository consulta
por requisição uma concessão exata Principal + Operation + Project.

`mcpAuthorization` é composição privada em application. Não aceita Principal
como argumento: autentica, valida o request MCP-1, verifica o grant exato e
resolve ProjectID no registro confiável antes de invocar MCP-2. Identidade
ausente/inválida, falhas das ports, grant ausente/divergente, request/operação
inválido e projeto ausente/divergente negam acesso sem chamar READ. Erros das
ports não expõem material bruto. Cancelamento é verificado entre etapas.
MCP-2 mantém sua própria resolução e todos os gates READ existentes.

Channel != Principal. Nenhum Planner/Executor participa. Não há roles, grupos,
curingas, herança, configuração estática de grants, transporte, servidor, OAuth,
WRITE, auditoria ou MCP-4. Implementações confiáveis das ports e auditoria
fail-closed ainda são necessárias antes de exposição externa.

RED observado em Linux/Docker: tipos/constructor MCP-3 ausentes impediam
compilação dos testes. Testes cobrem sucesso das quatro operações, autenticação
e grants por requisição e rejeições sem atingir MCP-2.

GREEN observado em Linux/Docker (`dev-orchestrator-p6-5-validation`, rede
desabilitada, `.runtime` ocultado com tmpfs):

- `go test ./internal/application ./internal/application/readcontracts ./internal/ports -count=1 -timeout=120s`: PASS.
- `timeout 300 ./scripts/validate.sh`: PASS (exit 0: `go test ./...`, `go vet ./...`, `go build ./...`).
- `git diff --check` e checks no-index dos três arquivos Go novos: sem erros de whitespace.

## MCP-4 — Secure READ Runtime

Entrega interna: adapters `internal/adapters/localsecurity`, port `ReadAudit`,
`application.SecureReadRuntime` e `composition.NewMCPReadRuntime`. Nenhum
servidor, transporte, OAuth, Planner/Executor ou operação WRITE é adicionado.
MCP-3 compartilha seu método privado de autorização com o runtime; seu dispatch
privado conserva comportamento e testes. MCP-2 continua privado e intacto.
O runtime público expõe somente `Query(evidence, operation, request)`; sua
composição trusted injeta as ports READ existentes e não expõe o READ interno.

### Configuração e trust boundary

`composition.MCPReadConfig` recebe três caminhos absolutos, distintos e sem
symlinks: `AuthenticationFile`, `GrantsFile`, `AuditFile`. Diretórios devem
existir, pertencer à administração confiável e ficar fora dos workspaces de
projetos. Restrinja permissões/ACLs: Channel, Planner e Executor não devem
escrever nesses arquivos. Nenhum caminho ou Principal vem de argumentos READ.
O constructor valida autenticação/grants e tipo do destino de auditoria;
ausência/erro não possui defaults permissivos. O primeiro Query testa a escrita
durável da auditoria antes de autenticar. Filesystems Linux são o ambiente
validado; criação do arquivo de auditoria usa modo 0600 e Sync do diretório.

Formato de autenticação (placeholder de hash, não credencial funcional):

```json
{"credentials":[{"principalId":"reader","sha256":"<64 hex characters of SHA-256>"}]}
```

Gere o token fora do repositório com pelo menos 32 bytes de aleatoriedade
criptográfica; entregue seus bytes exatos como `AuthenticationEvidence.Material`.
Persista somente o SHA-256 hexadecimal desses bytes na configuração. Não use
senha humana: SHA-256 simples não é um password KDF. Não registre token/evidence
nem reutilize credenciais de Planner/Executor. Comparação percorre todos os
digests com `subtle.ConstantTimeCompare`; token ausente ou acima de 4096 bytes
nega acesso. Hash duplicado, identidade inválida ou configuração inteira
inválida nega acesso. Mais de um token distinto por Principal permite rotação.

Formato dos grants:

```json
{"grants":[{"principalId":"reader","operation":"project.status","projectId":"p"}]}
```

Só as quatro operações READ MCP-1 são aceitas. Tuplas devem ser exatas e únicas;
roles, groups, wildcards, herança e grants implícitos não existem. `{"grants":[]}`
é configuração válida que nega todos os acessos. Arrays ausentes/null, campos
desconhecidos/duplicados, entradas inválidas e falha de leitura negam acesso.
IDs têm até 128 bytes e não aceitam curingas, NUL/CR/LF ou espaços nas pontas.
Cada arquivo JSON é limitado a 64 KiB, com profundidade limitada.

Autenticação e grants são relidos por requisição. Para atualizar/revogar,
substitua atomicamente o arquivo administrativo completo e válido. Requisições
subsequentes usam a nova fonte; não há cache permissivo nem alteração do domínio.
Revogação não cancela retroativamente requisições já autorizadas.

### Auditoria e fail-closed

Fluxo: RECEIVED durável → AuthenticationPort → Principal → GrantRepository →
autorização MCP-3/registro confiável do projeto → AUTHORIZED durável → MCP-2 →
SUCCESS ou READ_FAILURE durável → resultado. Autenticação negada gera
AUTHENTICATION_DENIED; grant/projeto/request/operação negados geram
AUTHORIZATION_DENIED. Erros são classes fixas ACCESS_DENIED/READ_UNAVAILABLE.
Retornos públicos usam erro genérico e nunca resposta parcial.

Eventos JSONL contêm AttemptID aleatório interno, PrincipalID validado (vazio
antes de autenticação), operação (UNKNOWN se não aprovada), ProjectID,
CorrelationID, timestamp UTC, outcome e classe segura de erro. Metadados
inválidos são omitidos; nenhuma evidence, token, payload READ ou erro bruto é
persistido. AttemptID distingue tentativas mesmo com CorrelationID repetido.
SUCCESS significa READ concluído, não comprova entrega ao Channel.

Append exige Write/Sync/Close bem-sucedidos. Falha do primeiro evento bloqueia
autenticação; falha de AUTHORIZED bloqueia READ; falha do evento final descarta
a resposta, embora a leitura possa já ter ocorrido. Auditoria de negações usa
contexto independente com timeout de 5s, inclusive após cancelamento da entrada.
As ports devem respeitar contextos; chamadas síncronas de filesystem/Sync não
possuem interrupção rígida por timeout em caso de travamento do filesystem.

Use um writer/runtime por arquivo de auditoria: há serialização dentro da
instância, sem coordenação multiprocesso. Log limitado a 16 MiB; rotacione/
arquive administrativamente antes do teto. Cauda parcial sem newline bloqueia
novos appends, exigindo recuperação administrativa. Não há reparo automático.
Falha de storage impede garantir um evento final: RECEIVED/AUTHORIZED podem
permanecer sem término; isso nunca é tratado como sucesso. Auditoria local
não é tamper-proof contra o administrador do host e depende da durabilidade
do filesystem. Nenhuma escrita operacional de auditoria escreve no projeto.

### Próximos milestones

- MCP-5 — External MCP Gateway & Docker: servidor/transporte MCP, composição
  externa, configuração/runtime, Docker e health/readiness.
- MCP-6 — External READ E2E: Channel externo → MCP → Security → READ real →
  resposta, com testes positivos e negativos.

Esses milestones permanecem pendentes; MCP-4 não os implementa.

### Evidência MCP-4

Estado MCP-4: DONE, aprovado pelo Planner; sem commit nesta sessão.
RED Linux/Docker observado: Authentication/Grants adapters, ReadAudit e runtime
ausentes impediam compilação dos novos testes. RED comportamental adicional:
teste detectou append aceitando cauda parcial de auditoria; corrigido para deny.

GREEN Linux/Docker (`dev-orchestrator-p6-5-validation`, sem rede, `.runtime`
ocultado por tmpfs):

- `go test ./internal/application ./internal/application/readcontracts ./internal/ports ./internal/adapters/localsecurity ./internal/composition -count=1 -timeout=120s`: PASS.
- `go test -race ./internal/adapters/localsecurity ./internal/composition -run "TestAuditDurable|TestMCPReadComposition" -count=1 -timeout=120s`: PASS.
- `timeout 300 ./scripts/validate.sh`: PASS, exit 0 (`go test ./...`, `go vet ./...`, `go build ./...`).
- `git diff --check`, `git diff --cached --check` e checks no-index dos arquivos novos/não rastreados: sem erros de whitespace.

Cobertura inclui evidência válida/inválida/ausente e configuração inválida;
Principal resolvido sem seleção pelo caller; grants exatos, projeto/operação
divergentes, falhas de fonte e revogação refletida no mesmo runtime; auditoria
de sucesso/negações/READ failure; falha em RECEIVED/AUTHORIZED/resultado/negação;
cancelamento; falta de auditoria; escrita concorrente serializada, teto de log,
symlink e cauda parcial. Integração usa adapters reais de auth/grants/auditoria
com fontes de projeto/tasks controladas em testes, sem exposição externa.
Tokens válidos dos testes de adapters/composição são gerados em runtime; não
há token funcional plaintext em código/configuração/documentação de produção.
Testes verificam que evidence/hash/erro bruto não aparece nos eventos.

## MCP-5 — External MCP Adapter & Runtime

Decisão física aprovada: MCP é inbound adapter do mesmo Dev Orchestrator.
`cmd/orchestrator` e `composition.Service` existentes hospedam o listener e o
lifecycle; reusam ProjectRepository, TaskRepository, LocalAgentDispatcher,
READ capabilities e o lease operacional `instance.lock`. Não há Gateway,
runtime, processo ou container separado para Security/Audit/Application.
Uma imagem/container de Orchestrator fornece esse deployment READ.

### Protocolo e entrada

SDK oficial Go `github.com/modelcontextprotocol/go-sdk v1.8.0`, compatível com
Go 1.25, encapsulado em `internal/adapters/mcp`. MCP `2025-11-25`, Streamable
HTTP stateless com resposta JSON em `/mcp`; initialize/list/call seguem o SDK
oficial. Não há sessão persistente como autoridade de identidade. GET/DELETE
do MCP endpoint não são suportados nesta composição; sem SSE legado, retomada
ou notificações assíncronas. Clientes devem configurar a versão suportada.
Fontes: [SDK/release](https://github.com/modelcontextprotocol/go-sdk/releases/tag/v1.8.0)
e [transporte oficial](https://modelcontextprotocol.io/specification/2025-11-25/basic/transports).

Somente `project.status`, `project.tasks`, `git.status`, `execution.status`
são tools. Argumentos seguem os DTOs MCP-1, inclusive correlationId e paginação;
Principal/workspace/credentials em argumentos são rejeitados. Evidence vem de
um único header `Authorization: Bearer <opaque token>`; não vai em tool
arguments, URL ou IDs de sessão. Bytes do token seguem para AuthenticationPort
através de SecureReadRuntime MCP-4. O adapter não consulta grants/repos nem
autentica/autoriza por conta própria. Discovery estático do protocolo/tools
não lê projetos e não concede acesso; todo tools/call READ válido exige MCP-4.

Fluxo real: cliente MCP → adapter → MCP-4 → autenticação → Principal → grant
exato → projeto confiável → auditoria durável → MCP-2 → auditoria do outcome →
CallToolResult (JSON estruturado e TextContent compatível). execution.status
continua UNAVAILABLE sem inferência de TaskStatus. Nenhum WRITE/task creation
ou chamada a Planner/Executor é introduzido.

### Configuração e administração

O JSON do Orchestrator ganha `DisableDiscord` (default false) e `MCP` opcional.
Configuração anterior continua válida. `DisableDiscord:true` exige MCP
configurado; nesse modo não são carregados token Discord, provider runtime ou
credenciais de Planner/Executor. `DisableDiscord:false` preserva a composição
Discord existente e pode habilitar MCP no mesmo processo. Seleção dos providers
continua independente dos Channels; nenhum Channel escolhe Principal/provider.

Exemplo de configuração pública (arquivos administrativos provisionados à parte):

```json
{
  "StateDir": "/var/lib/dev-orchestrator",
  "DisableDiscord": true,
  "RequestTimeout": "30s",
  "ShutdownTimeout": "30s",
  "MaxIntentBytes": 4096,
  "MaxEvidenceBytes": 32768,
  "Projects": [{"ID":"p","Name":"Pilot","Workspace":"/projects/p"}],
  "MCP": {
    "Listen": "0.0.0.0:8080",
    "Concurrency": 2,
    "Security": {
      "AuthenticationFile": "/run/mcp/security/auth.json",
      "GrantsFile": "/run/mcp/security/grants.json",
      "AuditFile": "/var/lib/dev-orchestrator/mcp-audit.jsonl"
    }
  }
}
```

StateDir é privado (0700), persistente e fora dos workspaces. O audit JSONL
fica diretamente nesse diretório, separado de tasks.json/audit.json. O lease
existente impede um segundo runtime/writer sobre esse armazenamento; lease
residual após crash exige inspeção/recuperação administrativa existente, sem
remoção automática. Auth/grants ficam fora de StateDir, workspaces e provider
runtime-auth; arquivos regulares sem symlink/hardlink, pertencentes ao UID do
runtime e sem permissões para grupo/outros (0400/0600). Configuração crítica
inválida interrompe startup antes de abrir o listener. Security paths não vêm
do Channel. `MCPGODEBUG` não pode alterar os defaults de transporte do SDK.

Em produção Linux, workspaces e auth/grants devem estar montados read-only;
o gate existente inspeciona isolamento. Diretório administrativo pode ser
montado read-only no container enquanto a administração atualiza sua fonte.
Use substituição atômica de auth/grants, mantendo ownership/permissões. Grants
e autenticação são relidos por chamada, preservando revogação MCP-4. Não monte
arquivos isolados se precisar refletir substituição de inode: monte o diretório.
Não bakear secrets na image; `.runtime`, secrets e auth fixtures são excluídos
do build context. Tokens funcionais dos testes são gerados em runtime.

### Lifecycle, health e falhas

START → validar configuração/security/isolation → adquirir lease/repositories
→ compor MCP-4 → comprovar Write/Sync de auditoria com RECEIVED de startup
(correlationId=runtime-startup) → abrir listener → READY → servir. O probe usa
o próprio writer MCP-4, sem criar sistema de auditoria paralelo.

SIGINT/SIGTERM marca o runtime como indisponível, cancela trabalho, fecha o
listener e aguarda shutdown limitado; falha/timeout fecha conexões e retorna
falha controlada. No shutdown bem-sucedido, recursos/lease existentes são
fechados após o trabalho. Se o shutdown HTTP exceder o prazo, o Service retorna
antes de fechar o lease; a próxima inicialização exige recuperação administrativa,
como em uma parada incompleta. Fechar conexões não garante interrupção de Sync
ou de outra operação síncrona de filesystem já em andamento.
`/healthz` retorna apenas LIVE; `/readyz` retorna READY quando o listener e
runtime seguro inicializado estão ativos, ou NOT_READY durante parada/falha.
Readiness comprova inicialização, não garante sucesso de uma consulta futura;
mudança/falha de storage continua negada por requisição. Nenhum status expõe
paths, hashes ou credenciais. `orchestrator -mcp-health <local health URL>` é
cliente de healthcheck limitado, sem iniciar outro runtime. `-check-config`
valida sem conectar externamente; não comprova escrita de auditoria/readiness.

| Condição | Resposta externa segura |
| --- | --- |
| JSON/protocolo inválido | Erro HTTP/JSON-RPC de protocolo sem detalhe bruto. |
| Tool/operação desconhecida | Erro JSON-RPC de unsupported/invalid params conforme SDK; nenhum READ. |
| Argumentos READ inválidos | Invalid arguments; nenhum READ. |
| Evidence ausente/inválida, grant/source/projeto inválido | CallToolResult IsError: READ denied or unavailable, com CorrelationID. |
| Audit/READ failure | Mesmo erro seguro de tool; outcomes internos distinguem as condições. |
| Panic inesperado | Erro interno genérico, sem stack trace. |
| Limite de corpo/resposta | 413 ou erro seguro; sem resposta parcial. |
| Saturação/shutdown | 503 Busy/Unavailable; novo trabalho não é aceito. |

Erro de repository/adapter nunca atravessa a entrada. Negação não revela se
um projeto existe; auditoria e CorrelationID permitem investigação interna.
Rejeições de parsing/protocolo não são tentativas READ válidas; auth/grants e
outcomes de chamadas válidas seguem integralmente o writer MCP-4.

### Limites e transporte local

- Corpo MCP: 64 KiB; envelope de resposta: 256 KiB; projeção MCP-1: 64 KiB.
- Header HTTP: 8 KiB; evidence: 4096 bytes; batch JSON-RPC não aceito.
- IDs: contrato interno limitado a 128 bytes. O schema MCP usa maxLength=128
  caracteres; IDs multibyte que excedam 128 bytes são rejeitados pelo contrato.
- Concurrency configurável 1–16 (exemplo 2); saturação retorna Busy sem fila ilimitada.
- RequestTimeout/ShutdownTimeout positivos, até 120s em MCP; trabalho usa contextos.
- HTTP ReadHeaderTimeout 5s, ReadTimeout configurado, WriteTimeout=request+15s,
  IdleTimeout 15s. Persistência síncrona mantém a limitação de timeout MCP-4.
- Paginação project.tasks: limite 1–100, offset até 10000; fonte completa antes
  da paginação permanece limitação do repository existente.

Este deployment é local: Host deve ser localhost/127.0.0.1/::1; Origin presente
deve ser exatamente a origem HTTP local da requisição. DNS rebinding/cross-origin
são negados; sem CORS permissivo. Docker publica apenas em 127.0.0.1. Dentro do
container pode escutar 0.0.0.0. HTTP Bearer local não é OAuth e não oferece TLS;
não publicar esse endpoint diretamente na Internet/LAN nem encaminhar headers
de origem/proxy como autoridade. HTTPS/exposição externa/compatibilidade ChatGPT
ficam no milestone MCP-6, sem implementação nesta entrega.

### Docker e teste local reproduzível

`Dockerfile` fornece targets validation (default preservado), build e runtime
(binário Orchestrator + Git/CA, sem código/config/secrets na image final).
`deploy/mcp-read-only.compose.yml` executa um único Orchestrator, raiz read-only,
caps removidas, no-new-privileges, audit persistente e healthcheck; nenhum socket
Docker/provider auth é necessário para READ puro. O compose P6 existente segue
usando validation para preservar seu fluxo anterior.

Para deployment MCP-7, use a operação PowerShell acima. Compose recebe
MCP_CONFIG_FILE, MCP_PROJECT_WORKSPACE, MCP_SECURITY_VOLUME e
MCP_OPERATIONAL_VOLUME; security usa volume Linux, sem MCP_SECURITY_DIR.
Os arquivos auth/grants usam exatamente o formato MCP-4; não há token plaintext
no exemplo. Compose exige fontes já existentes e storage provisionado.

Para repetir o vertical slice sem credenciais de produção, em shell Linux no
repositório (use nomes novos para volumes/container em cada ensaio):

```sh
docker build --target validation -t dev-orchestrator-mcp5-validation .
docker build --target runtime -t dev-orchestrator-mcp-read-only .
docker run --rm --network none -v "$PWD:/app" --tmpfs /app/.runtime -v mcp5-fixture:/fixture -v mcp5-state:/state -w /app dev-orchestrator-mcp5-validation go run ./internal/composition/testdata/mcp_client prepare
docker run --rm -d --name mcp5-local --network none --read-only --cap-drop ALL --security-opt no-new-privileges --tmpfs /tmp:rw,nosuid,nodev,size=64m -v mcp5-fixture:/fixture:ro -v mcp5-state:/state dev-orchestrator-mcp-read-only -config /fixture/orchestrator.json
docker exec mcp5-local orchestrator -mcp-health http://127.0.0.1:8080/readyz
docker run --rm --network container:mcp5-local -v "$PWD:/app:ro" --tmpfs /app/.runtime -v mcp5-fixture:/fixture:ro -v mcp5-state:/state:ro -w /app dev-orchestrator-mcp5-validation go run ./internal/composition/testdata/mcp_client probe
docker stop --timeout 20 mcp5-local
```

O harness apenas provisiona fixtures aleatórias e atua como cliente MCP oficial,
não cria servidor/runtime adicional. O token gerado fica em volume privado de
teste, nunca em stdout/repo/image. Probe verifica as quatro tools, projeção real
do projeto, Git real, UNAVAILABLE e projeto não autorizado negado/auditado.
Volumes de fixture/state são mantidos para inspeção administrativa.

Troubleshooting mínimo: startup genérico/NOT_READY → verificar configurações,
UID/permissões/mounts read-only, lease e audit tail/teto; Busy → ajustar concorrência
dentro do limite ou reduzir chamadas; READ IsError → investigar CorrelationID
na auditoria sem registrar evidence; Git falha → conferir gates Git existentes,
sem contorná-los. Não há fallback para credenciais/provider ou grants implícitos.

MCP-6 permanece NEXT para Channel externo real/ChatGPT e infraestrutura externa
que for aprovada; MCP-5 comprova somente cliente MCP local real.

### Evidência MCP-5 — 2026-10-07

Estado: implementado e validado para review do Planner; sem commit.
RED observado em Linux/Docker: testes de protocolo falharam por ausência do
adapter/constants; testes de runtime falharam por ausência de MCP config e
lifecycle/readiness no Service. GREEN final:

- `go test ./internal/adapters/mcp ./internal/composition ./cmd/orchestrator -count=1 -timeout=120s`: PASS.
- `go test -race ./internal/adapters/mcp ./internal/composition -run TestMCP -count=1 -timeout=120s`: PASS.
- `timeout 300 ./scripts/validate.sh`: PASS, exit 0 (suíte completa, vet, build).
- `docker build --target runtime -t dev-orchestrator-mcp-read-only .`: PASS.
- Compose MCP `config --quiet`: PASS; validação estrutural sem iniciar serviço.
- `git diff --check`, staged check e checks no-index dos novos arquivos: sem erros de whitespace.

Cliente oficial SDK local chama as quatro tools atravessando MCP-4/MCP-2. Teste
de adapter integrado usa counters do repositório: evidence ausente/inválida,
projeto/operação sem grant, source failure e audit failure produzem zero acesso
ao repositório, confirmando ausência de bypass. Rejeições de argumentos Principal/
workspace, tool desconhecida, origin/host, corpo excessivo, timeout, saturação e
shutdown estão cobertas; erros brutos não aparecem na resposta.

Vertical slice executado também no binário/imagem de deployment final, com
fixtures aleatórias em volumes Linux e sem providers externos. Container tinha
rootfs read-only, privileged=false, network=none e cap_drop=ALL; cliente usou
apenas o loopback compartilhado, sem portas publicadas ou socket Docker no
Orchestrator. `/readyz` passou após startup. Resultados do harness:

```text
MCP5_LOCAL_SLICE project.status PASS
MCP5_LOCAL_SLICE project.tasks PASS
MCP5_LOCAL_SLICE git.status PASS
MCP5_LOCAL_SLICE execution.status PASS
MCP5_LOCAL_SLICE unauthorized DENY_AUDITED
```

Git usou repositório real controlado montado read-only. execution.status retornou
UNAVAILABLE. Projeto unauthorized retornou IsError e AUTHORIZATION_DENIED, sem
AUTHORIZED/SUCCESS para sua correlação. SIGTERM via `docker stop --timeout 20`
produziu `READ_ONLY_SHUTDOWN PASS`; nova inicialização no mesmo storage também
passou, comprovando fechamento do lease e persistência da auditoria. Não houve
ChatGPT/Discord LIVE, OAuth, tunnel ou operação WRITE.
