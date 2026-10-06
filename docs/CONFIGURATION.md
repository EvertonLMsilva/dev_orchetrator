# Configuração P6.5 read-only

O entrypoint Linux recebe `-config <arquivo.json>`. O exemplo público está em
`internal/composition/testdata/p6.example.json`; substitua IDs e paths somente
por valores autorizados para o piloto. Não coloque segredos nesse arquivo.

| Campo | Contrato |
| --- | --- |
| StateDir | Diretório absoluto existente, privado (0700), separado dos workspaces e de runtime-auth |
| ApplicationID | Snowflake Discord; necessário para registro explícito de comandos |
| RequestTimeout | Duração positiva, por solicitação inteira; exemplo `75s` |
| ShutdownTimeout | Duração positiva para fechamento; exemplo `60s`, compatível com stop_grace_period |
| MaxIntentBytes | Positivo, até 8192; exemplo 4096 |
| MaxEvidenceBytes | Positivo, até 32768, reservando espaço no limite de entrada existente do Planner |
| Projects | IDs únicos, nome, workspace absoluto e parâmetros de evidência tipados |
| Routes | Pares únicos GuildID/ChannelID, ambos snowflakes válidos, vinculados a projeto existente |

Todos os campos de limites são explícitos; os valores do exemplo são escolhas
operacionais, não novos limites do provider. JSON inválido, campos desconhecidos,
chaves duplicadas (incluindo aliases de caixa), rotas ambíguas, paths com aliases
e sobreposição de storage/workspaces falham fechados.

`Evidence` permite somente SEARCH, READ_FILE, GIT_STATUS e GIT_DIFF. Cada ação
precisa constar explicitamente no mapa do projeto, incluindo GIT_STATUS. Não há
evidência habilitada implicitamente. Paths de leitura, busca e diff são relativos,
explícitos e confinados; a intenção/reason do Planner não fornece parâmetros.
Somente conteúdo público aprovado pode ocupar esses paths. Paths ocultos,
credenciais e chaves são negados; buscas também inspecionam descendentes.

O serviço exige montagem Linux read-only em todo o workspace, incluindo mounts
aninhados. Symlinks e arquivos especiais são recusados no perfil operacional.
Inspeções de isolamento/busca são limitadas a 100000 entradas. Git suporta somente
metadados locais e configuração inerte: core convencional, user.name/email,
remote URL/fetch/pushurl e branch remote/merge. Includes, filtros, fsmonitor,
drivers diff, extensions e outras chaves são recusados antes de comandos Git.

O token Discord é provisionado fora do repositório e lido de
`-discord-token-file` (padrão `/run/secrets/discord_token`), com limite de 4096
bytes. Nenhum token é armazenado em tasks/auditoria ou passado ao Planner.
Auth Planner continua exclusivamente em
`/var/lib/dev-orchestrator/runtime-auth`, administrada pelo runtime P6.4; não há
login, fallback ou importação de credencial host neste entrypoint.

O Compose usa P6_PILOT_WORKSPACE, P6_CONFIG_FILE, P6_OPERATIONAL_VOLUME,
P6_RUNTIME_AUTH_VOLUME e P6_DISCORD_TOKEN_FILE. Os volumes externos devem estar
provisionados; o volume auth é o já autorizado em P6.4. Não há configuração de
Executor, shell, ferramentas, retries ou hosts adicionais de provider.

`-check-config` valida configuração e paths sem conexão externa; a montagem
read-only e o Git seguro são novamente verificados ao construir o serviço.
`-register-commands` é uma operação Discord explícita e separada da inicialização.
Os dois modos não podem ser combinados. O perfil registra somente `/analyze`;
comandos legados de continuação/aprovação não são atendidos nesse perfil.

Operação, recuperação e kill switch: `docs/operations/P6_READ_ONLY.md`.
