# Decisões duráveis

-   O Dev Orchestrator é projeto separado do MKTauto.
-   Discord é interface e transporte, não source of truth.
-   Estado de tasks e execução é persistido pelo
    Orchestrator/repositório.
-   Planner e Executor são papéis separados.
-   ChatGPT/Planner concentra análise, arquitetura e especificação.
-   Codex/Executor recebe somente task fechada.
-   Agente local executa comandos controlados no PC e devolve evidência.
-   TDD e tasks atômicas são padrão.
-   Segurança usa allowlist, sandbox de paths e approval gates.
-   Autonomia será aumentada somente após o ciclo controlado estar
    comprovado.
-   Ports/adapters permitem trocar Planner, Executor e agente.
-   Arquitetura modular-first; microserviços somente com justificativa
    operacional.
-   MVP termina em P5.
-   Integrações concretas de ChatGPT/OpenAI e Codex serão verificadas
    contra interfaces oficiais atuais antes de implementação.
