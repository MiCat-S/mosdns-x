import * as api from "./api";
import * as app from "./app";
import * as components from "./components";
import * as healthPanel from "./health-panel";
import * as pages from "./pages";
import * as queryTrace from "./query-trace";
import * as runtimeConfig from "./runtime-config";
import * as shell from "./shell";
import * as usageChart from "./usage-chart";

// One table per source file keeps parallel edits apart; a key that appears
// in several files must be translated the same way in each (checked by
// i18n.test.ts).
export const localeParts = {
  api,
  app,
  components,
  "health-panel": healthPanel,
  pages,
  "query-trace": queryTrace,
  "runtime-config": runtimeConfig,
  shell,
  "usage-chart": usageChart,
};

const parts = Object.values(localeParts);
export const en: Record<string, string> = Object.assign(
  {},
  ...parts.map((part) => part.en),
);
export const ja: Record<string, string> = Object.assign(
  {},
  ...parts.map((part) => part.ja),
);
