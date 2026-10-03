# Configuração conceitual

Os nomes finais das variáveis serão definidos durante implementação.

Categorias previstas:

-   Discord: token/app/guild/canal permitido;
-   Project Registry: projetos e workspaces;
-   Planner: provider/model/credencial/budget;
-   Executor: provider/configuração/budget;
-   Local Agent: endpoint/identidade/política;
-   Limits: timeout, output, retries;
-   Storage: persistência do Orchestrator;
-   Audit: retenção/destino.

Segredos nunca entram no repositório. Criar `.env.example` somente
quando os nomes concretos forem implementados.
