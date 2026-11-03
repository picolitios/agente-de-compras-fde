import type { ToolEvent } from "../types";

// ToolTimeline separa decisões do agente das verificações feitas pelo backend.
export default function ToolTimeline({
  events,
  title,
  result,
}: {
  events?: ToolEvent[];
  title: string;
  result?: string;
}) {
  if (!events?.length) return null;
  return (
    <section className="tool-timeline" aria-label={title}>
      <strong>{title}</strong>
      <ol>
        {events.map((event, index) => (
          <li key={`${event.ferramenta}-${index}`}>
            <span className={`tool-origin ${event.origem}`}>
              {event.origem === "agente" ? "Agente" : "Backend"}
            </span>
            <code>
              {event.ferramenta}
              {event.produto_id
                ? `(${event.produto_id})`
                : event.termo
                  ? `("${event.termo}")`
                  : "()"}
            </code>
          </li>
        ))}
      </ol>
      {result && <p>{result}</p>}
    </section>
  );
}
