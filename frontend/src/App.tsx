import { useEffect, useRef, useState, type FormEvent } from "react";
import { confirmProposal, listPurchases, sendMessage } from "./api";
import ProposalCard from "./components/ProposalCard";
import HistoryPanel from "./components/HistoryPanel";
import type { ChatResponse, Purchase } from "./types";

interface Message {
  id: number;
  role: "user" | "assistant";
  text: string;
  reply?: ChatResponse;
  purchase?: Purchase;
}

const suggestions = [
  "Preciso de 3 laptops até US$ 2.000 cada.",
  "Quero 2 smartphones Samsung com boa câmera.",
];

// App coordena apenas estado visual; o agente, as regras e a persistência ficam na API.
export default function App() {
  const [draft, setDraft] = useState("");
  const [messages, setMessages] = useState<Message[]>([]);
  const [conversationID, setConversationID] = useState("");
  const [activeProposalID, setActiveProposalID] = useState<string | null>(null);
  const [sending, setSending] = useState(false);
  const [confirmingID, setConfirmingID] = useState<string | null>(null);
  const [chatError, setChatError] = useState("");
  const [history, setHistory] = useState<Purchase[]>([]);
  const [historyLoading, setHistoryLoading] = useState(true);
  const [historyError, setHistoryError] = useState("");
  const [historyOffset, setHistoryOffset] = useState(0);
  const [historyVersion, setHistoryVersion] = useState(0);
  const [hasMore, setHasMore] = useState(false);
  const nextID = useRef(0);
  const bottomRef = useRef<HTMLDivElement>(null);

  // O histórico é relido após a confirmação e quando a página muda.
  useEffect(() => {
    let canceled = false;
    setHistoryLoading(true);
    setHistoryError("");
    listPurchases(historyOffset)
      .then((result) => {
        if (canceled) return;
        setHistory(result.requisicoes);
        setHasMore(result.requisicoes.length === 10);
      })
      .catch((error: Error) => {
        if (!canceled) setHistoryError(error.message);
      })
      .finally(() => {
        if (!canceled) setHistoryLoading(false);
      });
    return () => {
      canceled = true;
    };
  }, [historyOffset, historyVersion]);

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: "smooth" });
  }, [messages, sending]);

  // O envio preserva o texto quando há falha para permitir uma nova tentativa.
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const text = draft.trim();
    if (!text || sending || confirmingID) return;
    const id = ++nextID.current;
    setChatError("");
    setSending(true);
    setMessages((previous) => [...previous, { id, role: "user", text }]);
    try {
      const reply = await sendMessage(text, conversationID);
      setConversationID(reply.conversa_id);
      setActiveProposalID(reply.proposta?.id || null);
      setMessages((previous) => [
        ...previous,
        {
          id: ++nextID.current,
          role: "assistant",
          text: reply.mensagem,
          reply,
        },
      ]);
      setDraft("");
    } catch (error) {
      setMessages((previous) =>
        previous.filter((message) => message.id !== id),
      );
      setChatError(
        error instanceof Error
          ? error.message
          : "Não foi possível enviar o pedido.",
      );
    } finally {
      setSending(false);
    }
  }

  // A confirmação só marca sucesso após receber o número registrado no servidor.
  async function confirm(id: string) {
    if (confirmingID || sending) return;
    setChatError("");
    setConfirmingID(id);
    try {
      const purchase = await confirmProposal(id);
      setMessages((previous) =>
        previous.map((message) =>
          message.reply?.proposta?.id === id
            ? { ...message, purchase }
            : message,
        ),
      );
      setActiveProposalID(null);
      setHistoryOffset(0);
      setHistoryVersion((version) => version + 1);
    } catch (error) {
      setChatError(
        error instanceof Error
          ? error.message
          : "Não foi possível confirmar a proposta.",
      );
    } finally {
      setConfirmingID(null);
    }
  }

  return (
    <div className="app-shell">
      <header className="app-header">
        <div className="brand-mark" aria-hidden="true">
          A<span>·</span>
        </div>
        <div className="brand-copy">
          <strong>Agente de compras</strong>
          <span>Forward Deployed Engineer · MVP</span>
        </div>
        <span className="demo-badge">
          <i /> Catálogo de demonstração
        </span>
      </header>
      <main className="workspace">
        <section className="chat-panel" aria-labelledby="chat-title">
          <div className="chat-heading">
            <div>
              <span className="eyebrow">Assistente de requisições</span>
              <h1 id="chat-title">O que você precisa comprar?</h1>
              <p>
                Descreva os produtos, a quantidade e seus critérios. A proposta
                será conferida antes do registro.
              </p>
            </div>
          </div>
          <div
            className="conversation"
            aria-live="polite"
            aria-relevant="additions text"
          >
            {messages.length === 0 && (
              <div className="welcome">
                <div className="welcome-icon" aria-hidden="true">
                  ✳
                </div>
                <h2>Comece com um pedido</h2>
                <p>
                  Eu consulto o catálogo de demonstração, verifico seus
                  critérios e preparo uma requisição para sua confirmação.
                </p>
                <div className="suggestions">
                  {suggestions.map((suggestion) => (
                    <button
                      type="button"
                      key={suggestion}
                      onClick={() => setDraft(suggestion)}
                    >
                      {suggestion}
                      <span aria-hidden="true">↗</span>
                    </button>
                  ))}
                </div>
              </div>
            )}
            {messages.map((message) => (
              <div key={message.id} className={`message-row ${message.role}`}>
                {message.role === "assistant" && (
                  <span className="avatar" aria-hidden="true">
                    A
                  </span>
                )}
                <div className="message-content">
                  <div className="message-bubble">{message.text}</div>
                  {message.reply && (
                    <ProposalCard
                      reply={message.reply}
                      active={activeProposalID === message.reply.proposta?.id}
                      confirming={confirmingID === message.reply.proposta?.id}
                      purchase={message.purchase}
                      onConfirm={confirm}
                    />
                  )}
                </div>
              </div>
            ))}
            {sending && (
              <div className="message-row assistant">
                <span className="avatar" aria-hidden="true">
                  A
                </span>
                <div className="message-bubble waiting" role="status">
                  Consultando catálogo e verificando critérios
                  <span className="dots">…</span>
                </div>
              </div>
            )}
            <div ref={bottomRef} />
          </div>
          <div className="composer-wrap">
            {chatError && (
              <div className="chat-error" role="alert">
                {chatError}
              </div>
            )}
            <form className="composer" onSubmit={submit}>
              <label htmlFor="request-message" className="sr-only">
                Descreva sua necessidade de compra
              </label>
              <textarea
                id="request-message"
                value={draft}
                maxLength={4000}
                rows={2}
                placeholder="Ex.: preciso de 3 laptops até US$ 2.000 cada…"
                onChange={(event) => setDraft(event.target.value)}
                onKeyDown={(event) => {
                  if (event.key === "Enter" && !event.shiftKey) {
                    event.preventDefault();
                    event.currentTarget.form?.requestSubmit();
                  }
                }}
                disabled={sending || Boolean(confirmingID)}
              />
              <button
                type="submit"
                className="primary send-button"
                disabled={!draft.trim() || sending || Boolean(confirmingID)}
                aria-label="Enviar pedido"
              >
                Enviar <span aria-hidden="true">↗</span>
              </button>
            </form>
            <p className="composer-note">
              Enter para enviar · Shift + Enter para quebrar linha · Nenhuma
              compra real será executada
            </p>
          </div>
        </section>
        <HistoryPanel
          purchases={history}
          loading={historyLoading}
          error={historyError}
          offset={historyOffset}
          hasMore={hasMore}
          onPage={setHistoryOffset}
          onRetry={() => setHistoryVersion((version) => version + 1)}
        />
      </main>
    </div>
  );
}
