import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { createBrowserRouter, RouterProvider } from "react-router-dom";
import { App } from "./App";
import { SessionProvider } from "./session";
import { LanguageProvider } from "./i18n";
import "@fontsource-variable/source-serif-4/opsz.css";
import "./fonts/fonts.css";
import "./styles.css";
const router = createBrowserRouter([
  {
    path: "*",
    element: (
      <SessionProvider>
        <App />
      </SessionProvider>
    ),
  },
]);

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <LanguageProvider>
      <RouterProvider router={router} />
    </LanguageProvider>
  </StrictMode>,
);
