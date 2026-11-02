# Agente de compras FDE

MVP para registrar requisições de compra a partir de pedidos em linguagem natural.
O projeto terá quatro serviços: backend Go com Google ADK, servidor MCP Go para
consultar a DummyJSON, PostgreSQL e interface React.

## Estado

Estrutura inicial. A implementação e as instruções de execução serão adicionadas
em commits focados por componente.

## Configuração

Copie `.env.example` para `.env` e preencha a chave OpenRouter apenas no arquivo
local. O arquivo `.env` não deve ser versionado. O backend não recebe valores
financeiros calculados pelo frontend ou pelo modelo como fonte de verdade.
