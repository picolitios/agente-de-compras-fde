# Agente de compras FDE

MVP de requisições de compra em linguagem natural. O agente Go com Google ADK consulta um catálogo de demonstração por MCP; o backend valida os produtos, calcula os valores em USD e grava a requisição no PostgreSQL somente após confirmação explícita. O registro **não realiza uma compra comercial**.

## Executar com Docker

Pré-requisito: Docker com Compose e uma chave OpenRouter válida para um modelo que aceite chamadas de ferramentas.

1. Copie `.env.example` para `.env`.
2. No `.env` local, preencha `OPENROUTER_API_KEY`, `POSTGRES_PASSWORD` e a mesma senha em `DATABASE_URL`. Nunca inclua o `.env` no Git.
3. Execute `docker compose up --build -d` na raiz do projeto.
4. Abra `http://localhost:5173`. A API fica em `http://localhost:8080` e o servidor MCP em `http://localhost:8081/mcp`.

Para conferir o estado, use `docker compose ps` e `curl http://localhost:8080/health`. Para encerrar, use `docker compose down`; o volume do PostgreSQL é preservado.

As portas externas ficam restritas a `127.0.0.1` e podem ser alteradas com `FRONTEND_PORT`, `API_PORT` e `MCP_PORT` no `.env`. O frontend encaminha `/api/*` para o serviço Go no Compose, sem expor a chave ao navegador. A aplicação rejeita uma chave ausente ou com o valor de exemplo.

### Modelo gratuito e alternativa

O padrão é `OPENROUTER_MODEL=google/gemma-4-31b-it:free`. O Gemma 4 aceita chamadas de ferramentas pela API OpenAI compatível do OpenRouter, usada pelo ADK Go. `OPENROUTER_FALLBACK_MODEL=openrouter/free` faz o OpenRouter tentar outro modelo gratuito quando o Gemma estiver indisponível ou limitado pelo provedor. O roteador gratuito pode escolher modelos diferentes entre chamadas; o backend continua revalidando os produtos e valores antes de criar uma proposta. Ambos os modelos são configuráveis sem alterar código. Defina `OPENROUTER_FALLBACK_MODEL=` para desativar a alternativa ou troque `OPENROUTER_MODEL` por outro modelo com suporte a ferramentas. Verifique preço e capacidade do modelo escolhido antes de usar um identificador pago.

Modelos `:free` têm limites de uso e podem receber HTTP 429 mesmo quando a chave e a integração estão corretas. Faça um pedido de teste antes da demonstração. Uma chave nova deve ser criada na conta OpenRouter do usuário e guardada apenas no `.env` local; a chave não é enviada ao frontend. Os testes automatizados verificam o contrato de ferramentas entre ADK, protocolo OpenAI e MCP sem consumir a API externa.

O tempo máximo de processamento de um pedido é configurado por `REQUEST_TIMEOUT` (90 segundos por padrão). O proxy do frontend aguarda até 100 segundos. Se o modelo gratuito atingir seu limite ou devolver uma resposta inválida, a API informa o motivo sem expor dados internos; o usuário pode tentar novamente.

## Fluxo

1. O usuário descreve categoria, quantidade e critérios no chat.
2. O agente decide quando chamar `buscar_produtos` e `detalhar_produto` no servidor MCP. Essas ferramentas consultam a DummyJSON.
3. O backend consulta as opções propostas pelo agente via MCP, confere os requisitos verificáveis e calcula os preços em centavos.
4. A interface mostra uma proposta com validade de 15 minutos. O usuário distribui a quantidade pedida entre as opções e confirma em um botão separado.
5. Na confirmação, o backend revalida cada produto escolhido, a quantidade exata e o orçamento total, e persiste os snapshots em transação. A requisição recebe um número `REQ-...` e aparece no histórico paginado.

O agente não tem acesso direto ao banco. O frontend envia apenas mensagem, ID de conversa e, na confirmação, ID da proposta com IDs de produtos e quantidades. Valores financeiros enviados pelo modelo ou navegador não são usados como fonte de verdade. Se um requisito não puder ser confirmado pelo catálogo, o fluxo pede esclarecimento. O catálogo usa USD; pedidos que exigem outra moeda precisam de esclarecimento.

## API

| Método | Rota | Uso |
| --- | --- | --- |
| `POST` | `/requisicoes` | Envia `{"mensagem":"...","conversa_id":""}`; o ID é opcional no primeiro pedido. |
| `POST` | `/requisicoes/{id}/confirmar` | Confirma com `{"itens":[{"produto_id":78,"quantidade":1}]}`. Sem corpo, confirma a sugestão original para compatibilidade. Não aceita preços do cliente. |
| `GET` | `/requisicoes?limit=20&offset=0` | Consulta requisições registradas. |
| `GET` | `/requisicoes/{id}` | Consulta uma requisição pelo ID numérico. |
| `GET` | `/health` | Verifica a disponibilidade da API e do banco. |

Erros da API usam `{"erro":{"codigo":"...","mensagem":"..."}}`. Propostas expiradas ou alteradas recebem HTTP 409; falhas de catálogo ou limite do provedor recebem 503. O backend limita tamanho da mensagem, duração da requisição, chamadas do modelo e paginação.

## Desenvolvimento e validação

- Backend: Go 1.26.6. Execute `go test ./...` e `go vet ./...`. O teste de integração PostgreSQL usa `TEST_DATABASE_URL`; sem ela, esse teste é ignorado.
- Frontend: Node 24. Em `frontend/`, execute `npm ci`, `npm run dev`, `npm run build` e `npm run format:check`. O Vite encaminha `/api` para `http://localhost:8080` no desenvolvimento local.
- Docker: `docker compose config -q` valida a configuração sem imprimir os valores interpolados. `docker compose build` valida as três imagens próprias.

Os testes cobrem catálogo, ferramentas MCP, interpretação por ADK com modelo simulado, validações do backend, API e persistência PostgreSQL. Uma execução real com OpenRouter depende da chave configurada somente no ambiente local.

## Limites do MVP

Há um agente, uma categoria por proposta e até cinco opções verificadas para distribuir a quantidade solicitada. As sessões de conversa ficam na memória da API, enquanto propostas e requisições ficam no PostgreSQL. O MVP não inclui autenticação, integrações reais de compra, RAG, filas, Kubernetes, ferramentas MCP de escrita ou avaliação automatizada de pedidos. Esses itens não são necessários para demonstrar a separação entre interpretação do modelo e validação transacional do backend.
