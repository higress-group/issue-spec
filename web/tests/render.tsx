import type { ReactElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { InspectorProvider } from "../src/app/problem-inspector";

export function renderApp(ui: ReactElement, route = "/") {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return {
    client,
    ...render(<QueryClientProvider client={client}><InspectorProvider><MemoryRouter initialEntries={[route]}>{ui}</MemoryRouter></InspectorProvider></QueryClientProvider>),
  };
}
