import type {
  ChatResponse,
  HistoryResponse,
  Purchase,
  Selection,
} from "./types";

interface APIError {
  erro?: { codigo?: string; mensagem?: string };
}

// request centraliza erros de rede e mensagens públicas devolvidas pela API.
async function request<T>(path: string, init?: RequestInit): Promise<T> {
  let response: Response;
  try {
    response = await fetch(`/api${path}`, init);
  } catch {
    throw new Error("Não foi possível conectar ao serviço. Tente novamente.");
  }
  const body = (await response.json().catch(() => null)) as
    (T & APIError) | null;
  if (!response.ok) {
    throw new Error(
      body?.erro?.mensagem || "O serviço não conseguiu concluir a solicitação.",
    );
  }
  if (body === null) {
    throw new Error("O serviço devolveu uma resposta inválida.");
  }
  return body;
}

// sendMessage envia só linguagem natural e o ID de conversa recebido da API.
export function sendMessage(
  message: string,
  conversationID: string,
): Promise<ChatResponse> {
  return request("/requisicoes", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ mensagem: message, conversa_id: conversationID }),
  });
}

// confirmProposal envia somente IDs e quantidades para revalidação no backend.
export function confirmProposal(
  id: string,
  selections: Selection[],
): Promise<Purchase> {
  return request(`/requisicoes/${encodeURIComponent(id)}/confirmar`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ itens: selections }),
  });
}

// listPurchases consulta páginas pequenas do histórico persistido.
export function listPurchases(
  offset: number,
  limit = 10,
): Promise<HistoryResponse> {
  return request(`/requisicoes?limit=${limit}&offset=${offset}`);
}
