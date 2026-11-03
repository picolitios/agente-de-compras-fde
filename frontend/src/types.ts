// Os tipos refletem apenas os campos usados da API; valores monetários vêm do backend.
export interface Product {
  id: number;
  title: string;
  description: string;
  category: string;
  price: string;
  brand: string;
}

export interface Item {
  product_id: number;
  product_name: string;
  unit_price_cents: number;
  quantity: number;
  subtotal_cents: number;
}

export interface Proposal {
  id: string;
  requirements: { quantity: number; max_total_cents?: number };
  items: Item[];
  options: Item[];
  total_cents: number;
  expires_at: string;
}

export interface Selection {
  produto_id: number;
  quantidade: number;
}

export interface Purchase {
  id: number;
  request_number: string;
  original_request: string;
  status: string;
  currency: string;
  total_cents: number;
  created_at: string;
  items: Item[];
  ferramentas?: string[];
}

export interface ChatResponse {
  tipo: "clarify" | "no_match" | "proposal";
  mensagem: string;
  conversa_id: string;
  ferramentas?: string[];
  produto_selecionado_id?: number;
  produtos?: Product[];
  proposta?: Proposal;
}

export interface HistoryResponse {
  requisicoes: Purchase[];
  limit: number;
  offset: number;
}
