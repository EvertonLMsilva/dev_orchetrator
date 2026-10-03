# Segurança

## Níveis

### AUTO

Somente ações explicitamente classificadas como baixo risco e confinadas
ao workspace. Exemplos iniciais: busca textual, leitura permitida,
listagem controlada, `git status`, `git diff` e testes previamente
autorizados.

### APPROVAL

Alterações ou efeitos relevantes: escrita fora do Executor controlado,
instalação de dependências, commit/push/branch/PR e comandos não
previamente classificados.

### BLOCKED

Por padrão: deleção destrutiva, comandos administrativos, acesso fora
dos workspaces, leitura de segredos, alteração de políticas de segurança
e shell arbitrário.

## Controles obrigatórios

-   workspace raiz por projeto;
-   canonicalização de paths e bloqueio de escape;
-   allowlist por ação, não apenas por string;
-   timeout;
-   limite de output;
-   limite de tentativas;
-   kill switch;
-   audit trail;
-   secrets fora de prompts/logs;
-   subprocessos sem privilégio administrativo por padrão.

## Regra crítica

Nunca executar texto gerado pelo modelo diretamente como shell sem
passar por parser, política e validação.
