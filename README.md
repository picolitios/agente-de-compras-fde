# Agente de compras FDE

MVP de requisições de compra em linguagem natural. O agente Go com Google ADK consulta um catálogo de demonstração por MCP; o backend valida os produtos, calcula os valores em USD e grava a requisição no PostgreSQL somente após confirmação explícita. O registro **não realiza uma compra comercial**.

## Executar com Docker

Pré-requisitos: Docker com Compose e [Ollama](https://docs.ollama.com/api/openai-compatibility) rodando no computador. O modelo local precisa aceitar chamadas de ferramentas; o padrão é `qwen3:4b-instruct` (download de aproximadamente 2,5 GB).

1. Copie `.env.example` para `.env`.
2. No `.env` local, preencha `POSTGRES_PASSWORD` e a mesma senha em `DATABASE_URL`. A chave OpenRouter não é necessária no modo local. Nunca inclua o `.env` no Git.
3. Inicie o Ollama e execute `ollama pull qwen3:4b-instruct` uma vez no computador.
4. Execute `docker compose up --build -d` na raiz do projeto.
5. Abra `http://localhost:5173`. A API fica em `http://localhost:8080` e o servidor MCP em `http://localhost:8081/mcp`.

Para conferir o estado, use `docker compose ps` e `curl http://localhost:8080/health`. Para encerrar, use `docker compose down`; o volume do PostgreSQL é preservado.

As portas externas ficam restritas a `127.0.0.1` e podem ser alteradas com `FRONTEND_PORT`, `API_PORT` e `MCP_PORT` no `.env`. A API acessa o Ollama do computador por `host.docker.internal:11434`; o Ollama não precisa ser exposto na rede externa. O frontend encaminha `/api/*` para o serviço Go no Compose. O modo local não injeta a chave OpenRouter no container da API.

### Voltar ao OpenRouter

Preencha `OPENROUTER_API_KEY` somente no `.env` local e execute `docker compose -f compose.yaml -f compose.openrouter.yaml up --build -d`. Para retornar ao modo local, execute `docker compose up --build -d`. Os dois modos usam o mesmo agente ADK, as mesmas ferramentas MCP, DummyJSON e PostgreSQL; apenas o endpoint do modelo muda.

O OpenRouter usa `OPENROUTER_MODEL=google/gemma-4-31b-it:free` e `OPENROUTER_FALLBACK_MODEL=openrouter/free` por padrão. Os modelos são configuráveis no `.env` sem alterar código. Defina `OPENROUTER_FALLBACK_MODEL=` para desativar a alternativa ou troque `OPENROUTER_MODEL` por outro modelo com suporte a ferramentas. Verifique preço e capacidade antes de escolher um modelo pago.

Modelos `:free` têm limites de uso e podem receber HTTP 429 mesmo quando a chave e a integração estão corretas. Faça um pedido de teste antes da demonstração. Uma chave nova deve ser criada na conta OpenRouter do usuário e guardada apenas no `.env` local; a chave não é enviada ao frontend. Os testes automatizados verificam o contrato de ferramentas entre ADK, protocolo OpenAI e MCP sem consumir a API externa.

O tempo máximo de processamento de um pedido é configurado por `REQUEST_TIMEOUT` (180 segundos por padrão no modo local). O proxy do frontend aguarda até 200 segundos. No modo local, respostas finais malformadas recebem uma única tentativa de correção em JSON, sem repetir as ferramentas; o backend valida os IDs mesmo quando o modelo contradiz a lista que forneceu. Modelos locais pequenos ainda podem interpretar requisitos de forma incorreta; o backend continua validando produtos, quantidade e preços antes de registrar uma requisição.

## Fluxo

1. O usuário descreve categoria, quantidade e critérios no chat.
2. O agente decide quando chamar `buscar_produtos` e `detalhar_produto` no servidor MCP. Essas ferramentas consultam a DummyJSON.
3. O backend consulta as opções propostas pelo agente via MCP e completa a lista de smartphones, laptops ou veículos com uma busca própria no mesmo catálogo. Quando o modelo omite categoria e quantidade ou responde `clarify` para um pedido inequívoco dessas categorias, o backend recupera um par explícito como `4 smartphones` ou um carro no singular em um pedido simples como `carro até 100 dólares`. Para `carros` sem quantidade, verifica a existência e pede o número de unidades antes de propor. Um limite unitário simples e inequívoco no texto também prevalece sobre um valor omitido pelo modelo. Cada opção é revalidada por ID, filtrada pelos requisitos verificáveis e calculada em centavos.
4. A interface mostra uma proposta com validade de 15 minutos. O usuário distribui pelo menos a quantidade pedida entre as opções, podendo aumentar o total até 1.000 unidades, e confirma em um botão separado.
5. Na confirmação, o backend revalida cada produto escolhido, a quantidade mínima e o orçamento total, e persiste os snapshots em transação. A requisição recebe um número `REQ-...` e aparece no histórico paginado. Se nenhum item atender, o backend consulta novamente o catálogo: distingue produto indisponível de limite de preço baixo, apresenta preços verificados próximos e sugere alterar o pedido. A busca por carros usa a categoria `vehicle`, pois a busca textual da DummyJSON não encontra esses modelos.

Quando o chat sugere aumentar um limite unitário ou exibe uma proposta ainda não confirmada, uma resposta curta na mesma conversa, como `pode ser 25000,00 então`, `aumente para 30000,00` ou `aumento pra 60000,00`, conserva o produto e a quantidade anteriores mesmo após vários ajustes e cria uma nova proposta com opções revalidadas. Após uma sugestão de preço sem proposta, `sim, pode aumentar` usa o menor preço informado; após uma proposta, informe o novo valor. Esse contexto temporário expira em 15 minutos, é mantido apenas na memória da API e não transfere critérios para outra conversa ou para um pedido novo.

Pedidos singulares simples como `carro 100 dólares` ou `um carro de 100 dólares` significam uma unidade e são comparados com os preços reais do catálogo. Quando uma consulta plural como `carros até 100 dólares` ainda precisa de quantidade, uma resposta curta na mesma conversa, como `1`, `quero um carro só` ou `um carro, pode ser 25000`, completa o pedido sem repetir a pergunta. Uma resposta que reafirma a mesma quantidade junto com o novo teto, como `aumente pra 25000,00, preciso de um carro só`, também mantém o escopo anterior. Mudanças de produto, quantidade ou outros critérios são interpretadas como um novo pedido.

Na avaliação local, `qwen3:4b-instruct` respondeu corretamente aos diálogos de preço e quantidade, troca de categoria, pedidos de laptops e celulares, marca obrigatória, moeda diferente e produto ausente. O backend ainda valida cada resultado: a avaliação melhora a demonstração, mas não equivale a treinar os pesos do modelo nem garante que toda resposta futura será correta.

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
- Conversas com o modelo local: após subir o Compose, execute `pwsh -NoProfile -File scripts/eval_conversations.ps1` para conferir os nove cenários sem registrar compras. Use `-Scenario preco_e_quantidade` para reproduzir apenas a conversa de carro e preço ou `-Repeat 2` para procurar falhas intermitentes; o script retorna erro quando um cenário falha.
- Frontend: Node 24. Em `frontend/`, execute `npm ci`, `npm run dev`, `npm run build` e `npm run format:check`. O Vite encaminha `/api` para `http://localhost:8080` no desenvolvimento local.
- Docker: `docker compose config -q` valida a configuração local sem imprimir os valores interpolados. `docker compose build` valida as três imagens próprias.

Os testes cobrem catálogo, ferramentas MCP, interpretação por ADK com modelo simulado, validações do backend, API e persistência PostgreSQL. O modo local permite testar o agente real sem chave externa; o modo OpenRouter depende da chave configurada somente no `.env` local.

## Limites do MVP

Há um agente, uma categoria por proposta, até cinco opções verificadas e até 1.000 unidades por requisição. As sessões de conversa ficam na memória da API, enquanto propostas e requisições ficam no PostgreSQL. O MVP não inclui autenticação, integrações reais de compra, RAG, filas, Kubernetes, ferramentas MCP de escrita ou avaliação automatizada de pedidos. Esses itens não são necessários para demonstrar a separação entre interpretação do modelo e validação transacional do backend.
