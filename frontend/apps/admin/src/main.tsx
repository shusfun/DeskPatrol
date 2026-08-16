import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { ThemeProvider, initializeTheme } from "@deskpatrol/design-tokens";
import "@deskpatrol/ui-admin/styles.css";
import "./styles.css";
import { App } from "./app";

initializeTheme();
createRoot(document.getElementById("root")!).render(<StrictMode><ThemeProvider><App /></ThemeProvider></StrictMode>);
