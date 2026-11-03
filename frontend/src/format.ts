// money exibe os centavos calculados pelo backend na moeda fixa do catálogo.
export function money(cents: number): string {
  return new Intl.NumberFormat("pt-BR", {
    style: "currency",
    currency: "USD",
  }).format(cents / 100);
}

// dateTime apresenta datas em português mantendo o instante original no atributo HTML.
export function dateTime(value: string): string {
  return new Intl.DateTimeFormat("pt-BR", {
    dateStyle: "short",
    timeStyle: "short",
  }).format(new Date(value));
}
