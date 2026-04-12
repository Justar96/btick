import { createRouter, createRoute, createRootRoute } from "@tanstack/react-router";
import { RootLayout } from "./routes/__root";
import { HomePage } from "./routes/index";
import { CoinPage } from "./routes/coin.$symbol";
import { ApiPage } from "./routes/api";

const rootRoute = createRootRoute({
  component: RootLayout,
});

const indexRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/",
  component: HomePage,
});

const coinRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/$symbol",
  component: CoinPage,
});

const apiRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/api",
  component: ApiPage,
});

const routeTree = rootRoute.addChildren([indexRoute, coinRoute, apiRoute]);

export const router = createRouter({ routeTree });

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
