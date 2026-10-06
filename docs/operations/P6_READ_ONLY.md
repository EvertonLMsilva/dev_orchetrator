# P6.5 — operação read-only

P6.5 não executa LIVE. Os comandos externos de registro/start abaixo só devem
ser usados no ensaio P6.6 explicitamente autorizado, depois do review da evidência.

## Provisionamento

Use Linux/Docker, imagens P6.4 já disponíveis e sessão runtime-owned existente.
Não copie auth do host, não execute login e não altere proxy/network policy.
Defina os cinco parâmetros Compose documentados em `docs/CONFIGURATION.md`.
O JSON é público e externo ao workspace piloto; o token Discord é secret externo.
O piloto precisa ter Git local convencional e paths de evidência públicos
aprovados. Worktrees com gitfile, symlinks e configuração Git com efeitos não
são suportados por este perfil.

Construa a imagem operacional com `docker compose -f
deploy/p6-read-only.compose.yml build`. Prepare somente o volume operacional
próprio, sem tocar no volume auth:

```sh
docker run --rm --network none \
  -v "${P6_OPERATIONAL_VOLUME}:/var/lib/dev-orchestrator" \
  dev-orchestrator-p6-read-only \
  install -d -m 0700 /var/lib/dev-orchestrator /var/lib/dev-orchestrator/state
```

O Compose monta o piloto read-only, o config read-only, storage próprio gravável
e o volume auth existente no path fixo do runtime. O socket Docker é uma
dependência confiável exclusiva da infraestrutura existente; não é exposto como
capacidade ao Discord/Planner. A imagem usa rootfs read-only, capabilities
removidas, no-new-privileges e nenhum restart automático.

Pré-valide sem inferência ou conexão Discord:

```sh
docker compose -f deploy/p6-read-only.compose.yml run --rm --no-deps \
  orchestrator sh -c 'go build -o /tmp/orchestrator ./cmd/orchestrator && exec /tmp/orchestrator -config /run/config/p6.json -check-config'
```

## Ensaio posterior autorizado

Registro é explícito, não ocorre em NewGateway/Open ou no start normal:

```sh
docker compose -f deploy/p6-read-only.compose.yml run --rm --no-deps \
  orchestrator sh -c 'go build -o /tmp/orchestrator ./cmd/orchestrator && exec /tmp/orchestrator -config /run/config/p6.json -register-commands'
docker compose -f deploy/p6-read-only.compose.yml up -d orchestrator
```

No guild/canal configurado, envie `/analyze intent:<intenção declarativa>`.
O callback envia acknowledgement deferred/ephemeral antes da inferência.
Guild/canal do evento determinam o projeto; texto não concede autoridade.
Há uma solicitação ativa e nenhuma fila: concorrentes recebem BUSY. O ciclo
permite duas decisões e uma evidência; pedidos posteriores de evidência retornam
LIMITED. PREPARE_EXECUTOR em qualquer rodada e RUN_TESTS retornam bloqueio.
Não existe Executor composto nem transição READY_FOR_CODEX/IN_PROGRESS.

A abertura do gateway também tem somente uma tentativa de conexão por instância.
O gate de identify impede redial do loop inicial do SDK, mantendo seu limiter na
primeira tentativa. Auto-reconnect continua desabilitado e REST usa max retries 0.
Nova tentativa operacional requer novo start explícito, nunca retry automático.

A resposta contém somente mensagem fixa, categoria de evidência quando coletada
e referência aleatória. Reasons, conteúdo bruto e erros internos não atravessam
o Discord. Falha inesperada ao publicar a resposta encerra a admissão e o
processo sem sucesso; não há retry. Logs dos SDKs são descartados para impedir
vazamento de payload/token. A auditoria guarda metadados próprios sanitizados.

## Estado, auditoria e recuperação

`state/tasks.json` guarda IDs/projeto, título fixo e estado de análise. Uma análise
encerrada fica BLOCKED, inclusive quando coleta evidência com sucesso: não é
uma task de execução concluída. Uma nova solicitação humana pode retomar análise
pelas transições existentes do WorkflowEngine.

`state/audit.json` registra START/RUN/FINISH, correlação, guild/canal, IDs, resultado público,
contadores, decisão categórica e tipo/status da evidência. Não guarda secrets,
intent/prompt, reason ou conteúdo bruto. Snapshots usam arquivo temporário exclusivo,
fsync, rename atômico e fsync do diretório. Cada snapshot tem teto de 1 MiB;
capacidade esgotada falha fechada, sem descarte/rotação automática.

`state/instance.lock` impede outra instância. Shutdown limpo o libera somente
após operações terminarem e o runtime fechar. Crash, timeout de cleanup ou
artefatos pending conservam uma condição fail-closed. Restart rejeita JSON
inválido, tasks órfãs/ambíguas, estados de Executor e auditoria incompleta.
Após falha, não remova locks nem edite snapshots automaticamente: preserve
evidência, confirme ausência de processos/leases e devolva a recuperação ao
operador/Planner. Nunca repare auth ou repita uma inferência incerta.

## Kill switch

SIGINT/SIGTERM cancela admissão, inferência e evidência. O entrypoint aguarda
workers/cleanup dentro de ShutdownTimeout e só informa SHUTDOWN PASS quando os
fechamentos passam. O startup compila em /tmp e usa exec, garantindo que o
processo Go receba o sinal diretamente.

```sh
docker compose -f deploy/p6-read-only.compose.yml stop orchestrator
```

Mantenha stop_grace_period compatível com ShutdownTimeout (ambos 60s no exemplo).
Uma falha/timeout não é shutdown PASS e não libera exclusão antecipadamente.

## Validação determinística

O aceite é Linux/Docker, sem LIVE e sem montar auth provisionada ou socket Docker
nos containers de testes. Execute `./scripts/validate.sh` com rede desabilitada.
O teste integrado de isolamento usa P6_TEST_PILOT apontando para uma fixture Git
separada, montada read-only. Ele exercita as quatro capacidades, compara hashes
de workspace/.git e comprova negação pelo kernel das escritas de fixture.
Esse parâmetro só existe em arquivos de teste, não seleciona projeto em produção.

A evidência P6.5 está em `docs/evidence/P6.5.md`. P6.6 deve prover somente o
config/canal/piloto autorizado e executar o ciclo real controlado sobre esta composição.
