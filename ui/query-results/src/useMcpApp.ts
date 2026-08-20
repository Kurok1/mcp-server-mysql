/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 1.2.1
 */
import { useEffect, useRef, useState } from "react";
import {
  App,
  applyDocumentTheme,
  applyHostFonts,
  applyHostStyleVariables,
  type McpUiHostContext,
} from "@modelcontextprotocol/ext-apps";
import type { ToolResultLike } from "./model";

export type MCPHandlers = {
  onToolInput: (sql: string) => void;
  onToolResult: (result: ToolResultLike) => void;
  onToolCancelled: (reason?: string) => void;
};

export type MCPAppState = {
  app: App | null;
  connected: boolean;
  connecting: boolean;
  connectionError: string;
  hostContext?: McpUiHostContext;
  canCallTools: boolean;
  preview: boolean;
};

function applyContext(context?: McpUiHostContext): void {
  if (!context) return;
  if (context.theme) applyDocumentTheme(context.theme);
  if (context.styles?.variables) applyHostStyleVariables(context.styles.variables);
  if (context.styles?.css?.fonts) applyHostFonts(context.styles.css.fonts);
  const insets = context.safeAreaInsets;
  if (insets) {
    const root = document.documentElement.style;
    root.setProperty("--safe-top", `${insets.top}px`);
    root.setProperty("--safe-right", `${insets.right}px`);
    root.setProperty("--safe-bottom", `${insets.bottom}px`);
    root.setProperty("--safe-left", `${insets.left}px`);
  }
}

export function useMcpApp(handlers: MCPHandlers): MCPAppState {
  const handlersRef = useRef(handlers);
  handlersRef.current = handlers;
  const [state, setState] = useState<MCPAppState>(() => {
    const preview = window.parent === window;
    return {
      app: null,
      connected: preview,
      connecting: !preview,
      connectionError: "",
      canCallTools: preview,
      preview,
    };
  });

  useEffect(() => {
    if (window.parent === window) {
      const params = new URLSearchParams(window.location.search);
      applyDocumentTheme(params.get("theme") === "dark" ? "dark" : "light");
      return;
    }

    let disposed = false;
    const app = new App(
      { name: "mcp-server-mysql-query-results", version: "2.0.0" },
      { availableDisplayModes: ["inline", "fullscreen"] },
      { autoResize: true, strict: true },
    );

    const onInput = (params: { arguments?: Record<string, unknown> }) => {
      handlersRef.current.onToolInput(
        typeof params.arguments?.sql === "string" ? params.arguments.sql : "",
      );
    };
    const onResult = (params: ToolResultLike) => handlersRef.current.onToolResult(params);
    const onCancelled = (params: { reason?: string }) =>
      handlersRef.current.onToolCancelled(params.reason);
    const onContext = (context: McpUiHostContext) => {
      applyContext(context);
      setState((current) => ({ ...current, hostContext: app.getHostContext() }));
    };

    app.addEventListener("toolinput", onInput);
    app.addEventListener("toolresult", onResult);
    app.addEventListener("toolcancelled", onCancelled);
    app.addEventListener("hostcontextchanged", onContext);

    void app
      .connect()
      .then(() => {
        if (disposed) return;
        const hostContext = app.getHostContext();
        applyContext(hostContext);
        setState({
          app,
          connected: true,
          connecting: false,
          connectionError: "",
          hostContext,
          canCallTools: Boolean(app.getHostCapabilities()?.serverTools),
          preview: false,
        });
      })
      .catch((error: unknown) => {
        if (disposed) return;
        setState({
          app: null,
          connected: false,
          connecting: false,
          connectionError: error instanceof Error ? error.message : String(error),
          canCallTools: false,
          preview: false,
        });
      });

    return () => {
      disposed = true;
      app.removeEventListener("toolinput", onInput);
      app.removeEventListener("toolresult", onResult);
      app.removeEventListener("toolcancelled", onCancelled);
      app.removeEventListener("hostcontextchanged", onContext);
      void app.close();
    };
  }, []);

  return state;
}
