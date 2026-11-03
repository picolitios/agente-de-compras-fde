import { useEffect, useState } from "react";
import { evaluateCase, getEvaluation } from "../api";
import type { EvaluationCatalog, EvaluationResult } from "../types";

type CaseState = {
  result?: EvaluationResult;
  error?: string;
  running?: boolean;
};

// EvaluationPanel executa cinco pedidos isolados sob demanda e conserva o placar na tela.
export default function EvaluationPanel() {
  const [catalog, setCatalog] = useState<EvaluationCatalog | null>(null);
  const [states, setStates] = useState<Record<string, CaseState>>({});
  const [loading, setLoading] = useState(true);
  const [running, setRunning] = useState(false);
  const [error, setError] = useState("");
  const [refresh, setRefresh] = useState(0);

  // A lista vem da API, fonte de verdade também usada pelo script PowerShell.
  useEffect(() => {
    let active = true;
    setLoading(true);
    setError("");
    getEvaluation()
      .then((data) => {
        if (active) setCatalog(data);
      })
      .catch((cause: Error) => {
        if (active) setError(cause.message);
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, [refresh]);

  async function runEvaluation() {
    if (!catalog || running) return;
    setRunning(true);
    setStates({});
    for (const test of catalog.casos) {
      setStates((current) => ({ ...current, [test.id]: { running: true } }));
      try {
        const result = await evaluateCase(test.id);
        setStates((current) => ({ ...current, [test.id]: { result } }));
      } catch (cause) {
        setStates((current) => ({
          ...current,
          [test.id]: {
            error:
              cause instanceof Error ? cause.message : "Falha na avaliação.",
          },
        }));
      }
    }
    setRunning(false);
  }

  const completed =
    catalog?.casos.filter((test) => {
      const state = states[test.id];
      return Boolean(state?.result || state?.error);
    }).length ?? 0;
  const correct =
    catalog?.casos.filter((test) => states[test.id]?.result?.acertou).length ??
    0;

  return (
    <section className="evaluation-panel" aria-labelledby="evaluation-title">
      <span className="eyebrow">Teste do agente</span>
      <div className="evaluation-heading">
        <h2 id="evaluation-title">5 pedidos</h2>
        <strong aria-live="polite">{completed ? correct : "—"}/5</strong>
      </div>
      {loading && <p className="muted">Carregando casos…</p>}
      {error && (
        <div className="inline-error" role="alert">
          <p>{error}</p>
          <button
            type="button"
            onClick={() => setRefresh((value) => value + 1)}
          >
            Tentar novamente
          </button>
        </div>
      )}
      {catalog && (
        <>
          <p className="evaluation-model">
            Agente: <b>{catalog.agente.nome}</b>
            <br />
            Modelo: <b>{catalog.agente.modelo}</b> · {catalog.agente.provedor}
          </p>
          <ol className="evaluation-cases">
            {catalog.casos.map((test) => {
              const state = states[test.id];
              return (
                <li key={test.id}>
                  <span>{test.pedido}</span>
                  <small
                    className={
                      state?.result?.acertou
                        ? "passed"
                        : state?.error || state?.result
                          ? "failed"
                          : ""
                    }
                  >
                    {state?.running
                      ? "Testando…"
                      : state?.result
                        ? state.result.acertou
                          ? "Acertou"
                          : "Errou"
                        : state?.error
                          ? "Falhou"
                          : "Pendente"}
                  </small>
                  {((state?.result && !state.result.acertou) ||
                    state?.error) && (
                    <em>{state.error || state.result?.motivo}</em>
                  )}
                </li>
              );
            })}
          </ol>
          <button
            className="evaluation-run"
            type="button"
            onClick={runEvaluation}
            disabled={running}
          >
            {running
              ? `Executando ${completed + 1} de 5…`
              : "Executar 5 pedidos"}
          </button>
          <p className="evaluation-note">
            Somente propostas de teste. Nenhuma requisição é confirmada.
          </p>
        </>
      )}
    </section>
  );
}
