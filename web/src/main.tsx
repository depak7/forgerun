import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { createBrowserRouter, RouterProvider } from "react-router-dom";
import { Layout } from "./components/Layout";
import { NewTaskPage } from "./pages/NewTask";
import { NotFoundPage } from "./pages/NotFound";
import { SettingsPage } from "./pages/Settings";
import { TasksPage } from "./pages/Tasks";
import { ThreadPage } from "./pages/Thread";
import "./styles/global.css";

// The app's pages. Paths match what the Go server sends back to index.html,
// so a reload on any of them works.
const router = createBrowserRouter([
  {
    element: <Layout />,
    children: [
      { path: "/", element: <TasksPage /> },
      { path: "/tasks/new", element: <NewTaskPage /> },
      { path: "/threads/:id", element: <ThreadPage /> },
      { path: "/settings", element: <SettingsPage /> },
      { path: "*", element: <NotFoundPage /> },
    ],
  },
]);

const queryClient = new QueryClient({
  defaultOptions: { queries: { retry: 1, refetchOnWindowFocus: true } },
});

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </StrictMode>,
);
