import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import { App } from "./App";
import { Api } from "./lib/api";
import { SessionProvider } from "./session";
import "./styles.css";

const api = new Api();

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <SessionProvider api={api}>
      <App />
    </SessionProvider>
  </StrictMode>,
);
